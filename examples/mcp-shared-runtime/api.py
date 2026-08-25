#!/usr/bin/env python3
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

started = time.monotonic()
print("database connection refused; retrying", file=sys.stderr, flush=True)
time.sleep(0.4)
print("database connection restored", flush=True)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/ready" and time.monotonic() - started >= 0.8:
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ready\n")
            return
        self.send_response(503)
        self.end_headers()

    def log_message(self, _format, *_args):
        return


print("listening on :18931", flush=True)
HTTPServer(("127.0.0.1", 18931), Handler).serve_forever()
