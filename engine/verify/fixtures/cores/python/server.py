#!/usr/bin/env python3
"""The same Python core, served over HTTP (stdlib only).

    python3 server.py [port]        # 0 = pick a free port; prints "LISTENING <port>"
    BOTKIT_TOKEN=secret python3 server.py 8080

POST /v1/call  {"capability": "...", "input": {...}}  ->  {"output": ...} | {"error": {...}}
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from core import handle

TOKEN = os.environ.get("BOTKIT_TOKEN", "")
STATUS = {"invalid_input": 400, "unavailable": 503, "internal": 500}


class Handler(BaseHTTPRequestHandler):
    def _send(self, status, obj):
        body = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/v1/call":
            return self._send(404, {"error": {"code": "unknown_capability"}})
        if TOKEN and self.headers.get("Authorization") != "Bearer " + TOKEN:
            return self._send(401, {"error": {"code": "unauthorized"}})
        try:
            n = int(self.headers.get("Content-Length", "0"))
            req = json.loads(self.rfile.read(n))
        except Exception:
            return self._send(400, {"error": {"code": "invalid_input"}})
        resp = handle(req)
        status = 200 if "output" in resp else STATUS.get(resp["error"]["code"], 422)
        self._send(status, resp)

    def log_message(self, *args):  # quiet
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8080
    srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print("LISTENING", srv.server_address[1], flush=True)
    srv.serve_forever()
