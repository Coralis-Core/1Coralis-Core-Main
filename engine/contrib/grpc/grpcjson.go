// Package grpcjson is the gRPC+JSON transport adapter.
//
//	Server: NewServer exposes a capability.Router to other processes/languages over gRPC.
//	Client: Remote makes a remote core look like any other provider.
//
// Wire format (one service method, any language):
//
//	service Platform {
//	  rpc Call(CallRequest) returns (CallResponse) {}
//	}
//	message CallRequest {
//	  string capability = 1;
//	  bytes input = 2;
//	  map<string,string> meta = 3;
//	}
//	message CallResponse {
//	  bytes output = 1;
//	  string error_code = 2;
//	  string error_message = 3;
//	}
//
// Status codes: invalid_input, unauthorized, unknown_capability, a domain error
// declared in the contract, unavailable, internal. Contracts are enforced on
// BOTH sides through contract.Guard.
package grpcjson

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/contract"
)

// ---- server ----------------------------------------------------------------

type Options struct {
	Token    string // if set, requests need "authorization: Bearer <Token>" metadata
	Log      *slog.Logger
	Allow    func(capability string) bool // gates which capabilities are served
	MaxDepth int                          // if > 0, refuses calls through that many platforms
	Timeout  time.Duration                // per-call timeout; default 10s
}

// Server implements gRPC service for the platform.
type Server struct {
	Set  *contract.Set
	R    *capability.Router
	Opts Options
}

// Call handles a single capability invocation over gRPC.
// Input: {capability: string, input: bytes, meta: map[string]string}
// Output: {output: bytes, error_code: string}
func (s *Server) Call(ctx context.Context, capName string, input []byte, meta map[string]string) ([]byte, string, error) {
	if s.Opts.Token != "" {
		// Handle() copies the gRPC "authorization" metadata header into meta; a value
		// in the request body is ignored there, so the body cannot forge it.
		auth := meta["authorization"]
		if auth == "" {
			return nil, "unauthorized", nil
		}
		got := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.Opts.Token)) != 1 {
			return nil, "unauthorized", nil
		}
	}

	if s.Opts.Allow != nil && !s.Opts.Allow(capName) {
		return nil, "unknown_capability", nil
	}

	c, ok := s.Set.Get(capName)
	if !ok || c.Kind != contract.KindRequest {
		return nil, "unknown_capability", nil
	}

	if len(input) == 0 {
		return nil, "invalid_input", nil
	}

	// Depth: refuse a call that has already crossed MaxDepth platforms (loop guard).
	if meta == nil {
		meta = map[string]string{}
	}
	d, _ := parseDepth(meta["depth"])
	if s.Opts.MaxDepth > 0 && d >= s.Opts.MaxDepth {
		return nil, "depth_exceeded", nil
	}
	meta["depth"] = fmt.Sprintf("%d", d+1)

	ctx = capability.WithMeta(ctx, meta)

	// Check if fanout mode
	if s.R.PolicyOf(capability.Name(c.Capability)).Mode == capability.ModeFanout {
		return s.serveFanout(c, input, ctx)
	}

	prov := contract.Guard(c, func(ctx context.Context, in []byte) ([]byte, string, error) {
		out, err := s.R.CallJSON(ctx, capability.Name(c.Capability), in)
		if err == nil {
			return out, "", nil
		}
		return nil, wireCode(c.Capability, err), nil
	})

	out, code, err := prov(ctx, input)
	switch {
	case err != nil:
		s.Opts.Log.Error("grpc: contract violation", "capability", c.Capability, "err", err)
		return nil, "internal", nil
	case code != "":
		return nil, code, nil
	}
	return out, "", nil
}

func (s *Server) serveFanout(c *contract.Contract, input []byte, ctx context.Context) ([]byte, string, error) {
	if err := c.ValidateInput(input); err != nil {
		return nil, "invalid_input", nil
	}

	res, err := s.R.CallAllJSON(ctx, capability.Name(c.Capability), input)
	if err != nil {
		code := wireCode(c.Capability, err)
		if !c.Declares(code) {
			s.Opts.Log.Error("grpc: undeclared error from fanout", "capability", c.Capability, "code", code)
			code = "internal"
		}
		return nil, code, nil
	}

	type fanErr struct {
		Code string `json:"code"`
	}
	type fanItem struct {
		Source string          `json:"source"`
		Output json.RawMessage `json:"output,omitempty"`
		Error  *fanErr         `json:"error,omitempty"`
	}

	items := make([]fanItem, len(res))
	for i, rs := range res {
		it := fanItem{Source: fmt.Sprintf("%d", i+1)}
		switch {
		case rs.Err != nil:
			code := wireCode(c.Capability, rs.Err)
			if !c.Declares(code) {
				s.Opts.Log.Error("grpc: undeclared error from fanout source", "capability", c.Capability, "code", code)
				code = "internal"
			}
			it.Error = &fanErr{Code: code}
		default:
			if verr := c.ValidateOutput(rs.Output); verr != nil {
				s.Opts.Log.Error("grpc: fanout source violates contract", "capability", c.Capability, "err", verr)
				it.Error = &fanErr{Code: "internal"}
			} else {
				it.Output = rs.Output
			}
		}
		items[i] = it
	}

	agg, _ := json.Marshal(struct {
		Results []fanItem `json:"results"`
	}{items})

	return agg, "", nil
}

// ---- client ----------------------------------------------------------------

// Remote talks to a remote core that implements the gRPC format above.
type Remote struct {
	Conn    *grpc.ClientConn
	Token   string
	Timeout time.Duration
}

// Dial creates a PLAINTEXT gRPC connection (localhost / private network only).
func Dial(target string) (*Remote, error) { return dial(target, insecure.NewCredentials()) }

// DialTLS creates a gRPC connection secured with TLS (system root CAs).
func DialTLS(target string) (*Remote, error) {
	return dial(target, credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12}))
}

func dial(target string, creds credentials.TransportCredentials) (*Remote, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	return &Remote{Conn: conn, Timeout: 10 * time.Second}, nil
}

// Bind registers the remote core as provider for each capability.
func (rm *Remote) Bind(r *capability.Router, name string, set *contract.Set, capabilities ...string) error {
	for _, cn := range capabilities {
		c, ok := set.Get(cn)
		if !ok || c.Kind != contract.KindRequest {
			return fmt.Errorf("grpcjson: no request contract for %q", cn)
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

// ---- helpers ---------------------------------------------------------------

func wireCode(cap string, err error) string {
	if err == nil {
		return ""
	}
	if code := capability.CodeOf(err); code != "" {
		return code
	}
	return "internal"
}

func parseDepth(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	var d int
	_, err := fmt.Sscanf(s, "%d", &d)
	return d, err
}
