"""Python adapter: the per-language connection to the platform (stdlib only).

A core written in Python registers handlers; the adapter serves the HTTP+JSON
wire format (POST /v1/call) and answers core.v1.describe / core.v1.health.
Wire: {"capability","input","meta"} -> {"output"} or {"error":{"code","message"}}.
"""
import json
import hmac
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CAP_DESCRIBE = "core.v1.describe"
CAP_HEALTH = "core.v1.health"


class CoreError(Exception):
    """Domain error: code must be one declared in the capability's contract."""
    def __init__(self, code, message=""):
        super().__init__(message or code)
        self.code, self.message = code, message


class Core:
    def __init__(self, name, version="1", token_env=None):
        self.name, self.version = name, version
        self.token = os.environ.get(token_env, "") if token_env else ""
        self.handlers = {}
        self.health = lambda: ("ok", "")

    def capability(self, name):
        def deco(fn):
            self.handlers[name] = fn
            return fn
        return deco

    def call(self, capability, payload, meta):
        """Returns (http_status, body_dict)."""
        if capability == CAP_DESCRIBE:
            return 200, {"output": {"name": self.name, "version": self.version,
                                    "capabilities": sorted(self.handlers),
                                    "publishes": [], "subscribes": []}}
        if capability == CAP_HEALTH:
            st, detail = self.health()
            out = {"status": st}
            if detail:
                out["detail"] = detail
            return 200, {"output": out}
        fn = self.handlers.get(capability)
        if fn is None:
            return 404, {"error": {"code": "unknown_capability"}}
        try:
            return 200, {"output": fn(payload, meta)}
        except CoreError as e:
            return 422, {"error": {"code": e.code, "message": e.message}}
        except Exception:  # never leak internals
            return 500, {"error": {"code": "internal"}}

    def serve(self, host="127.0.0.1", port=9001):
        core = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, status, body):
                data = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self):
                if self.path != "/v1/call":
                    return self._send(404, {"error": {"code": "unknown_capability"}})
                if core.token:
                    got = self.headers.get("Authorization", "").removeprefix("Bearer ")
                    if not hmac.compare_digest(got, core.token):
                        return self._send(401, {"error": {"code": "unauthorized"}})
                try:
                    n = int(self.headers.get("Content-Length", "0"))
                    if n > 1 << 20:
                        return self._send(400, {"error": {"code": "invalid_input"}})
                    req = json.loads(self.rfile.read(n))
                    cap, payload = req["capability"], req.get("input", {})
                except Exception:
                    return self._send(400, {"error": {"code": "invalid_input"}})
                self._send(*core.call(cap, payload, req.get("meta") or {}))

        srv = ThreadingHTTPServer((host, port), H)
        srv.serve_forever()
