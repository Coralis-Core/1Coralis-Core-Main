"""Calls a platform's gRPC endpoint from Python with plain grpcio (pip install grpcio).
No generated stubs: the wire is JSON; method /platform.v1.Platform/Call.
Usage: python client.py 127.0.0.1:9000 identity.v1.user.get '{"id": 7}'  (token in env GRPC_TOKEN)"""
import json, os, sys
import grpc

def main(target, capability, payload):
    ch = grpc.insecure_channel(target)   # use grpc.secure_channel(...) for TLS
    call = ch.unary_unary("/platform.v1.Platform/Call",
                          request_serializer=lambda d: json.dumps(d).encode(),
                          response_deserializer=json.loads)
    md = [("authorization", "Bearer " + os.environ["GRPC_TOKEN"])] if os.environ.get("GRPC_TOKEN") else []
    print(call({"capability": capability, "input": json.loads(payload)}, metadata=md, timeout=10))

if __name__ == "__main__":
    main(*sys.argv[1:4])
