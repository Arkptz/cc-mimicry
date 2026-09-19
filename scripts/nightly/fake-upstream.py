#!/usr/bin/env python3
"""Minimal local upstream for nightly fingerprint captures.

Every request is answered with the Anthropic-shaped 401 the real API returns
for an invalid credential. The nightly capture only needs the OUTBOUND request
(formed before any response arrives), so a local mock makes the run
deterministic: no dependency on api.anthropic.com reachability, no repeated
fake-auth attempts against the real service, and the same non-firstParty
endpoint class the committed relay captures came from.

Usage: fake-upstream.py [--port 19099]
"""

from __future__ import annotations

import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

REJECT_BODY = {
    "type": "error",
    "error": {
        "type": "authentication_error",
        "message": "invalid x-api-key",
    },
}


class Handler(BaseHTTPRequestHandler):
    def _reject(self) -> None:
        body = json.dumps(REJECT_BODY).encode()
        self.send_response(401)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self) -> None:
        # Drain the request body so the client can finish sending it; the
        # interceptor has already recorded the full outbound request.
        length = int(self.headers.get("content-length") or 0)
        if length:
            self.rfile.read(min(length, 64 * 1024 * 1024))
        self._reject()

    def do_GET(self) -> None:
        self._reject()

    def log_message(self, fmt: str, *args: object) -> None:
        # One line per request; the capture log stays readable.
        print(f"fake-upstream: {self.command} {self.path}", flush=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=19099)
    parser.add_argument("--host", default="127.0.0.1")
    args = parser.parse_args()

    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"fake-upstream: listening on {args.host}:{args.port}", flush=True)
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
