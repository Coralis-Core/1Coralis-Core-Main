// Package httpjson is the HTTP+JSON transport adapter.
//
//	Server: NewHandler exposes a capability.Router to other processes/languages.
//	Client: Remote makes a remote core look like any other provider.
//
// Wire format (one endpoint, any language):
//
//	POST /v1/call   {"capability": "...", "input": {...}, "meta": {"key": "value"}}  (meta optional)
//	200             {"output": {...}}
//	4xx/5xx         {"error": {"code": "...", "message": "..."}}
//
// Status: 400 invalid_input, 401 unauthorized, 404 unknown_capability,
// 422 a domain error declared in the contract, 503 unavailable, 500 internal.
// Contracts are enforced on BOTH sides through contract.Guard. TLS is a
// deployment concern (terminate it in front, or give Remote an https URL).
package httpjson

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/contract"
)

const Path = "/v1/call"

type wireRequest struct {
	Capability string          `json:"capability"`
	Input      json.RawMessage `json:"input"`
	// Meta is the free key-value part of the envelope. It travels with the
	// call (into the handler's context, and on to any remote core) and is
	// never interpreted here.
	Meta map[string]string `json:"meta,omitempty"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type wireResponse struct {
	Output json.RawMessage `json:"output,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}

// ---- server ----------------------------------------------------------------

type Options struct {
	Token   string // if set, requests need "Authorization: Bearer <Token>"
	MaxBody int64  // default 1 MiB
	Log     *slog.Logger
	// Allow, if set, decides which capabilities this endpoint serves. A
	// capability it refuses looks EXACTLY like one that does not exist
	// (404 unknown_capability), so callers cannot probe what is behind it.
	Allow func(capability string) bool
	// MaxDepth, if > 0, refuses calls that already went through that many
	// platforms (loop / runaway nesting protection; default off).
	MaxDepth int
}

func NewHandler(set *contract.Set, r *capability.Router, o Options) http.Handler {
	if o.MaxBody <= 0 {
		o.MaxBody = 1 << 20
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc(Path, func(w http.ResponseWriter, req *http.Request) {
		serve(set, r, o, w, req)
	})
	return mux
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wireResponse{Error: &wireError{Code: code, Message: msg}})
}

func serve(set *contract.Set, r *capability.Router, o Options, w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid_input", "use POST")
		return
	}
	if o.Token != "" {
		got := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(o.Token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "bad or missing token")
			return
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, o.MaxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "invalid_input", "body too large or unreadable")
		return
	}
	var wr wireRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wr); err != nil || len(wr.Input) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_input", "body must be {\"capability\": ..., \"input\": ...}")
		return
	}
	if o.Allow != nil && !o.Allow(wr.Capability) {
		writeErr(w, http.StatusNotFound, "unknown_capability", wr.Capability)
		return
	}
	c, ok := set.Get(wr.Capability)
	if !ok || c.Kind != contract.KindRequest {
		writeErr(w, http.StatusNotFound, "unknown_capability", wr.Capability)
		return
	}
	if r.PolicyOf(capability.Name(c.Capability)).Mode == capability.ModeFanout {
		serveFanout(c, r, o, w, req, wr)
		return
	}
	prov := contract.Guard(c, func(ctx context.Context, in []byte) ([]byte, string, error) {
		out, err := r.CallJSON(ctx, capability.Name(c.Capability), in)
		if err == nil {
			return out, "", nil
		}
		return nil, wireCode(o, c.Capability, err), nil
	})
	meta, mcode := PrepareMeta(wr.Meta, o.MaxDepth)
	if mcode != "" {
		writeErr(w, statusFor(mcode), mcode, "")
		return
	}
	out, code, err := prov(capability.WithMeta(req.Context(), meta), wr.Input)
	switch {
	case err != nil:
		o.Log.Error("httpjson: contract violation by provider", "capability", c.Capability, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "")
	case code == "":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wireResponse{Output: out})
	default:
		writeErr(w, statusFor(code), code, "")
	}
}

// PrepareMeta makes sure a call carries a trace id and counts the hop. The
// platform manages these reserved keys itself; callers may pass their own
// trace id. A positive max refuses calls that are already too deep.
func PrepareMeta(in map[string]string, max int) (capability.Meta, string) {
	m := capability.Meta{}
	for k, v := range in {
		m[k] = v
	}
	if m[capability.MetaTrace] == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		m[capability.MetaTrace] = hex.EncodeToString(b[:])
	}
	depth, _ := strconv.Atoi(m[capability.MetaDepth])
	if depth < 0 {
		depth = 0
	}
	if max > 0 && depth >= max {
		return nil, "depth_exceeded"
	}
	m[capability.MetaDepth] = strconv.Itoa(depth + 1)
	return m, ""
}

// wireCode maps a call error to the code that may cross the wire: unavailable,
// a coded domain error, or internal (never leaking internals, which are logged).
func wireCode(o Options, capName string, err error) string {
	switch {
	case errors.Is(err, capability.ErrNoProvider), errors.Is(err, capability.ErrUnavailable),
		errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "unavailable"
	case errors.Is(err, capability.ErrAmbiguous):
		return "maybe_delivered"
	case capability.CodeOf(err) != "":
		return capability.CodeOf(err)
	default:
		o.Log.Error("httpjson: provider failed", "capability", capName, "err", err)
		return "internal"
	}
}

// fanItem is one source's outcome inside a fan-out answer. Source is an opaque
// label (the position in the configured order), never a core id: callers must
// not learn which cores exist or how they are named.
type fanItem struct {
	Source string          `json:"source"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}

// serveFanout answers a capability bound with mode fanout. One call yields one
// aggregate output {"results":[...]}; every source is still checked against the
// capability's own contract, so the contract holds per source. A source that
// fails or breaks its contract is set aside as an error entry (the others still
// answer); with on_error fail any failed source fails the whole call instead.
func serveFanout(c *contract.Contract, r *capability.Router, o Options, w http.ResponseWriter, req *http.Request, wr wireRequest) {
	if err := c.ValidateInput(wr.Input); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_input", "")
		return
	}
	meta, mcode := PrepareMeta(wr.Meta, o.MaxDepth)
	if mcode != "" {
		writeErr(w, statusFor(mcode), mcode, "")
		return
	}
	ctx := capability.WithMeta(req.Context(), meta)
	res, err := r.CallAllJSON(ctx, capability.Name(c.Capability), wr.Input)
	if err != nil {
		code := wireCode(o, c.Capability, err)
		if !c.Declares(code) {
			o.Log.Error("httpjson: undeclared error code from fan-out", "capability", c.Capability, "code", code)
			code = "internal"
		}
		writeErr(w, statusFor(code), code, "")
		return
	}
	items := make([]fanItem, len(res))
	for i, rs := range res {
		it := fanItem{Source: strconv.Itoa(i + 1)}
		switch {
		case rs.Err != nil:
			code := wireCode(o, c.Capability, rs.Err)
			if !c.Declares(code) {
				o.Log.Error("httpjson: undeclared error code from a fan-out source", "capability", c.Capability, "source", rs.Provider, "code", code)
				code = "internal"
			}
			it.Error = &wireError{Code: code}
		default:
			if verr := c.ValidateOutput(rs.Output); verr != nil {
				o.Log.Error("httpjson: fan-out source violates the contract", "capability", c.Capability, "source", rs.Provider, "err", verr)
				it.Error = &wireError{Code: "internal"}
			} else {
				it.Output = rs.Output
			}
		}
		items[i] = it
	}
	agg, _ := json.Marshal(struct {
		Results []fanItem `json:"results"`
	}{items})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(wireResponse{Output: agg})
}

func statusFor(code string) int {
	switch code {
	case "invalid_input":
		return http.StatusBadRequest
	case "unavailable", "maybe_delivered":
		return http.StatusServiceUnavailable
	case "depth_exceeded":
		return http.StatusLoopDetected
	case "internal":
		return http.StatusInternalServerError
	default:
		return http.StatusUnprocessableEntity // domain error declared in the contract
	}
}

// ---- client ----------------------------------------------------------------

// Remote talks to a remote core that implements the wire format above.
type Remote struct {
	BaseURL string
	Token   string
	Client  *http.Client // default: 10s timeout
}

func (rm Remote) client() *http.Client {
	if rm.Client != nil {
		return rm.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Provider is the raw (unguarded) call for one capability.
func (rm Remote) Provider(c *contract.Contract) contract.Provider {
	return func(ctx context.Context, in []byte) ([]byte, string, error) {
		payload, _ := json.Marshal(wireRequest{Capability: c.Capability, Input: in, Meta: capability.MetaFrom(ctx)})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(rm.BaseURL, "/")+Path, bytes.NewReader(payload))
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if rm.Token != "" {
			req.Header.Set("Authorization", "Bearer "+rm.Token)
		}
		resp, err := rm.client().Do(req)
		if err != nil {
			// Could not even connect: nothing was sent, always safe to retry or
			// fall back. Any other failure (timeout, reset) may have been processed.
			var op *net.OpError
			if errors.As(err, &op) && op.Op == "dial" {
				return nil, "unavailable", nil
			}
			return nil, "maybe_delivered", nil
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, "maybe_delivered", nil
		}
		var wr wireResponse
		if err := json.Unmarshal(raw, &wr); err != nil {
			if resp.StatusCode >= 500 {
				return nil, "maybe_delivered", nil
			}
			return nil, "", fmt.Errorf("remote %s: unreadable response (HTTP %d)", c.Capability, resp.StatusCode)
		}
		if wr.Error != nil {
			return nil, wr.Error.Code, nil
		}
		if resp.StatusCode != http.StatusOK || len(wr.Output) == 0 {
			return nil, "", fmt.Errorf("remote %s: HTTP %d without output", c.Capability, resp.StatusCode)
		}
		return wr.Output, "", nil
	}
}

// Bind registers the remote core as provider `name` for each capability, with
// the contract enforced on input (before sending) and output (after receiving).
func (rm Remote) Bind(r *capability.Router, name string, set *contract.Set, capabilities ...string) error {
	for _, cn := range capabilities {
		c, ok := set.Get(cn)
		if !ok || c.Kind != contract.KindRequest {
			return fmt.Errorf("httpjson: no request contract for %q", cn)
		}
		guarded := contract.Guard(c, rm.Provider(c))
		err := r.ProvideJSON(name, capability.Name(cn), func(ctx context.Context, in []byte) ([]byte, error) {
			out, code, err := guarded(ctx, in)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", capability.ErrInternal, err)
			}
			if code != "" {
				return nil, capability.NewError(code, "")
			}
			return out, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
