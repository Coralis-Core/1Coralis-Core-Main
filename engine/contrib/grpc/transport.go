package grpcjson

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/metadata"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/contract"
)

// Wire contract of the transport (no protobuf, no code generation: any language with
// gRPC can call it by using a JSON serializer and content-subtype "json"):
//
//	service  platform.v1.Platform
//	method   /platform.v1.Platform/Call            (unary)
//	content  application/grpc+json
//	request  {"capability": "...", "input": <json>, "meta": {"k": "v"}}
//	response {"output": <json>} or {"error_code": "...", "error_message": "..."}
//	auth     gRPC metadata "authorization: Bearer <token>"
const (
	ServiceName = "platform.v1.Platform"
	FullMethod  = "/platform.v1.Platform/Call"
	codecName   = "json"
)

type CallRequest struct {
	Capability string            `json:"capability"`
	Input      json.RawMessage   `json:"input"`
	Meta       map[string]string `json:"meta,omitempty"`
}

type CallResponse struct {
	Output       json.RawMessage `json:"output,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (jsonCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
func (jsonCodec) Name() string                       { return codecName }

func init() { encoding.RegisterCodec(jsonCodec{}) }

// ---- server side ------------------------------------------------------------

type callService interface {
	Handle(ctx context.Context, req *CallRequest) (*CallResponse, error)
}

var serviceDesc = grpc.ServiceDesc{
	ServiceName: ServiceName,
	HandlerType: (*callService)(nil),
	Methods:     []grpc.MethodDesc{{MethodName: "Call", Handler: callHandler}},
	Metadata:    "platform.v1",
}

func callHandler(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
	in := new(CallRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if ic == nil {
		return srv.(callService).Handle(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: FullMethod}
	return ic(ctx, in, info, func(ctx context.Context, r any) (any, error) {
		return srv.(callService).Handle(ctx, r.(*CallRequest))
	})
}

// NewServer builds a Server with safe defaults (logger, 10s timeout).
func NewServer(set *contract.Set, r *capability.Router, o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	return &Server{Set: set, R: r, Opts: o}
}

// Register mounts the service on a *grpc.Server.
func (s *Server) Register(g grpc.ServiceRegistrar) { g.RegisterService(&serviceDesc, s) }

// Handle implements the wire method: it takes the token from gRPC metadata only
// (never from the body) and maps the result to the response envelope.
func (s *Server) Handle(ctx context.Context, req *CallRequest) (*CallResponse, error) {
	meta := map[string]string{}
	for k, v := range req.Meta {
		if k != "authorization" {
			meta[k] = v
		}
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("authorization"); len(v) > 0 {
			meta["authorization"] = v[0]
		}
	}
	if s.Opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Opts.Timeout)
		defer cancel()
	}
	out, code, err := s.Call(ctx, req.Capability, req.Input, meta)
	if err != nil {
		return &CallResponse{ErrorCode: "internal"}, nil
	}
	if code != "" {
		return &CallResponse{ErrorCode: code}, nil
	}
	return &CallResponse{Output: out}, nil
}

// ---- client side ------------------------------------------------------------

// Provider returns a raw (unguarded) call for one capability. Transport failures
// (refused, timeout, unreachable) become the code "unavailable".
func (rm *Remote) Provider(c *contract.Contract) contract.Provider {
	return func(ctx context.Context, in []byte) ([]byte, string, error) {
		meta := map[string]string{}
		for k, v := range capability.MetaFrom(ctx) {
			if k != "authorization" {
				meta[k] = v
			}
		}
		if rm.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, rm.Timeout)
			defer cancel()
		}
		if rm.Token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+rm.Token)
		}
		var resp CallResponse
		err := rm.Conn.Invoke(ctx, FullMethod, &CallRequest{Capability: c.Capability, Input: in, Meta: meta}, &resp, grpc.CallContentSubtype(codecName))
		if err != nil {
			return nil, "unavailable", nil
		}
		if resp.ErrorCode != "" {
			return nil, resp.ErrorCode, nil
		}
		return resp.Output, "", nil
	}
}

// NewGRPCServer returns a *grpc.Server that speaks JSON for every client, including
// clients (Python, Node, ...) that send the plain "application/grpc" content type
// without the "+json" subtype. Use it for a dedicated platform port.
func NewGRPCServer(opts ...grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(append([]grpc.ServerOption{grpc.ForceServerCodec(jsonCodec{})}, opts...)...)
}
