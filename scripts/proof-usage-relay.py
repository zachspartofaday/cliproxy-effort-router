#!/usr/bin/env python3
"""Operator-only, after approval: localhost CPA relay exporting counters only.

Never saves a request/response body, header, identifier, error text or tool input.
Write --output underneath the operator's private proof prefix. No proxy env use.
HTTP/SSE only: a production WebSocket proof needs its own approved transport test.
"""
import argparse
import http.client
import http.server
import json
import pathlib
import threading

HOP = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
       "te", "trailer", "transfer-encoding", "upgrade", "host", "content-length"}
FIELDS = ("input_tokens", "output_tokens", "cached_tokens", "reasoning_tokens")

def counter(value):
    return value if type(value) is int and 0 <= value < 2**63 else None

def usage_record(payload):
    if not isinstance(payload, dict):
        return {}
    usage = payload.get("usage")
    if not isinstance(usage, dict):
        return {}
    result = {field: counter(usage.get(field)) for field in FIELDS[:2]}
    for source, target in (("input_tokens_details", "cached_tokens"),
                           ("output_tokens_details", "reasoning_tokens")):
        details = usage.get(source)
        result[target] = counter(details.get(target)) if isinstance(details, dict) else None
    # Claude's cache-read count is a separately named provider field.
    result["cache_read_input_tokens"] = counter(usage.get("cache_read_input_tokens"))
    return {key: value for key, value in result.items() if value is not None}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--mode", choices=("shadow", "active"), required=True)
    args = parser.parse_args()
    lock = threading.Lock()
    calls = {"codex": 0, "claude": 0}
    tokens = 0

    def observe(endpoint, status, counters):
        nonlocal tokens
        with lock:
            tokens += counters.get("output_tokens", 0)
            record = {"mode": args.mode, "endpoint": endpoint, "status": status,
                      "usage": counters, "usage_available": bool(counters)}
            with args.output.open("a", encoding="utf-8") as output:
                output.write(json.dumps(record) + "\n")

    class Relay(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self):
            self.forward()

        def do_POST(self):
            self.forward()

        def log_message(self, *args):
            pass

        def forward(self):
            endpoint = self.path.split("?", 1)[0]
            if endpoint not in ("/v1/models", "/v1/responses", "/v1/responses/compact", "/v1/messages"):
                self.send_error(404, "unsupported proof endpoint")
                return
            family = "claude" if endpoint == "/v1/messages" else "codex"
            if self.command == "POST":
                with lock:
                    if calls[family] >= (4 if family == "claude" else 40) or tokens >= 20000:
                        self.send_error(429, "proof budget exhausted")
                        return
                    calls[family] += 1
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length < 0 or length > 64 * 1024 * 1024 or self.headers.get("Transfer-Encoding"):
                    self.send_error(413, "unsupported proof body")
                    return
                body = self.rfile.read(length)
                headers = {key: value for key, value in self.headers.items() if key.lower() not in HOP}
                upstream = http.client.HTTPConnection("127.0.0.1", 8318, timeout=180)
                upstream.request(self.command, self.path, body=body, headers=headers)
                response = upstream.getresponse()
                self.send_response(response.status)
                for key, value in response.getheaders():
                    if key.lower() not in HOP:
                        self.send_header(key, value)
                self.send_header("Connection", "close")
                self.end_headers()
                self.close_connection = True
                counters = {}
                if "text/event-stream" in (response.getheader("Content-Type") or ""):
                    while True:
                        line = response.readline(16 * 1024 * 1024 + 1)
                        if not line:
                            break
                        if len(line) > 16 * 1024 * 1024:
                            raise ValueError("oversized event")
                        self.wfile.write(line)
                        self.wfile.flush()
                        if line.startswith(b"data: "):
                            try:
                                event = json.loads(line[6:])
                                if event.get("type") == "response.completed":
                                    counters.update(usage_record(event.get("response")))
                                elif event.get("type") in ("message_start", "message_delta"):
                                    counters.update(usage_record(event.get("message", event)))
                            except (ValueError, AttributeError):
                                pass
                else:
                    raw = response.read(64 * 1024 * 1024 + 1)
                    if len(raw) > 64 * 1024 * 1024:
                        raise ValueError("oversized response")
                    self.wfile.write(raw)
                    try:
                        counters = usage_record(json.loads(raw))
                    except ValueError:
                        pass
                observe(endpoint, response.status, counters)
                upstream.close()
            except Exception:
                # No repr(exception): upstream failures can contain sensitive text.
                self.close_connection = True
                observe(endpoint, 0, {})

    http.server.ThreadingHTTPServer(("127.0.0.1", 8319), Relay).serve_forever()

if __name__ == "__main__":
    main()
