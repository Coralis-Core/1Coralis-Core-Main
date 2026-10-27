# contrib/grpc — gRPC transport (separate Go module)

JSON over gRPC, **no protobuf and no code generation**. Any language with a gRPC library can call it.

    service  platform.v1.Platform      method  /platform.v1.Platform/Call   (unary)
    request  {"capability": "...", "input": <json>, "meta": {"k": "v"}}
    response {"output": <json>}  or  {"error_code": "...", "error_message": "..."}
    auth     gRPC metadata  authorization: Bearer <token>   (a token inside the body is ignored)

Serve a platform:

    srv := grpcjson.NewServer(contractSet, platform.Host().Router, grpcjson.Options{Token: tok, Allow: platform.Exposed, MaxDepth: 8})
    g := grpcjson.NewGRPCServer()   // speaks JSON to every client, also plain "application/grpc" ones
    srv.Register(g); g.Serve(listener)

Mount a remote platform/core (kind "grpc" is registered by the host program):

    o := boot.Options{Contracts: set}; grpcjson.Register(&o)
    {"id":"a","kind":"grpc","capabilities":["x.v1.y"],"options":{"target":"host:9000","token_env":"A_TOKEN","timeout_ms":3000,"tls":true}}

Behaviour: contracts are enforced on both sides; transport failures become `unavailable`; a call that
already crossed `MaxDepth` platforms is refused (`depth_exceeded`, loop guard); an `unauthorized` reply is
seen by the caller as `internal` (the code is not declared by contracts; same as the HTTP transport).
Default `Dial` is PLAINTEXT (localhost/private networks); set `"tls": true` for TLS (system CAs).

Not included: streaming, mTLS client certificates.

Python client example: `examples/grpc-python-client/client.py`.

Build/test (needs network for google.golang.org):  `go mod tidy && go test -race ./...`  — commit the generated go.sum.
