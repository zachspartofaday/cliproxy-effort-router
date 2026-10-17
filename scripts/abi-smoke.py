#!/usr/bin/env python3
"""Exercise the actual C ABI and adapter via a local mock CPA. No real keys.
Usage: python3 scripts/abi-smoke.py /abs/plugin.dylib /abs/adapter
"""
import base64
import ctypes as C
import http.server
import json
import pathlib
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("len", C.c_size_t)]
Call = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Free = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
Shutdown = C.CFUNCTYPE(None)
class Host(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("ctx", C.c_void_p), ("call", C.c_void_p), ("free", C.c_void_p)]
class Plugin(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("call", Call), ("free", Free), ("shutdown", Shutdown)]

calls = []
class MockCPA(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        assert self.path == "/v1/responses"
        assert self.headers["Authorization"] == "Bearer fake-smoke-only"
        assert self.headers["X-Opencode-Session"].startswith("effort-router-")
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert body["model"] == "ocg/deepseek-v4.1-flash"
        assert body["text"]["format"]["strict"] is True
        calls.append(body)
        response = json.dumps({"status": "completed", "output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": '{"effort":"high"}'}]}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(response)
    def log_message(self, *args):
        pass

def rpc(plugin, method, body):
    raw = json.dumps(body).encode()
    buf = Buffer()
    rc = plugin.call(method.encode(), raw, len(raw), C.byref(buf))
    try:
        envelope = json.loads(C.string_at(buf.ptr, buf.len))
    finally:
        plugin.free(buf.ptr, buf.len)
    assert rc == 0 and envelope["ok"], envelope
    return envelope["result"]

def main():
    lib = C.CDLL(str(pathlib.Path(sys.argv[1]).resolve()))
    lib.cliproxy_plugin_init.argtypes = [C.POINTER(Host), C.POINTER(Plugin)]
    plugin = Plugin()
    assert lib.cliproxy_plugin_init(C.byref(Host(99)), C.byref(plugin)) == 1
    assert lib.cliproxy_plugin_init(C.byref(Host(1)), C.byref(plugin)) == 0
    assert plugin.abi_version == 1
    mock = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MockCPA)
    threading.Thread(target=mock.serve_forever, daemon=True).start()
    # Reserve an ephemeral adapter port, then release it immediately before launch.
    reserve = http.server.HTTPServer(("127.0.0.1", 0), MockCPA)
    port = reserve.server_port
    reserve.server_close()
    with tempfile.TemporaryDirectory(prefix="effort-router-mock-") as directory:
        key = pathlib.Path(directory, "fake-key")
        key.write_text("fake-smoke-only")
        key.chmod(0o600)
        process = subprocess.Popen([str(pathlib.Path(sys.argv[2]).resolve()), "-backend", "ocg", "-listen", f"127.0.0.1:{port}", "-cpa-url", f"http://127.0.0.1:{mock.server_port}", "-credential-file", str(key)], stdout=subprocess.DEVNULL)
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=.1):
                        break
                except OSError:
                    time.sleep(.02)
            else:
                raise AssertionError("adapter did not start")
            config = f"jev-url: http://127.0.0.1:{port}/v1/systemone\nstate-file: {directory}/state.jsonl\nshadow: true\n"
            registration = rpc(plugin, "plugin.register", {"config_yaml": base64.b64encode(config.encode()).decode(), "schema_version": 6})
            assert registration["schema_version"] == 6
            assert registration["capabilities"] == {"request_normalizer": True}
            body = b'{ "messages": [{"role":"user","content":"shadow fake turn"}] }'
            request = {"FromFormat": "claude", "ToFormat": "claude", "Model": "claude-opus-5-5", "Stream": False, "Body": base64.b64encode(body).decode()}
            result = rpc(plugin, "request.normalize", request)
            assert base64.b64decode(result["Body"]) == body
            assert len(calls) == 1
            config = config.replace("shadow: true", "shadow: false")
            rpc(plugin, "plugin.reconfigure", {"config_yaml": base64.b64encode(config.encode()).decode(), "schema_version": 6})
            request["Body"] = base64.b64encode(body.replace(b"shadow fake turn", b"active fake turn")).decode()
            result = rpc(plugin, "request.normalize", request)
            decoded = json.loads(base64.b64decode(result["Body"]))
            assert decoded["messages"][0]["output_config"]["effort"] == "high"
            assert len(calls) == 2
            rpc(plugin, "plugin.quiesce", {})
            rpc(plugin, "plugin.shutdown", {})
            plugin.shutdown()
            print("ABI 1 / schema 6: register, shadow, adapter -> mock CPA, reconfigure, active, shutdown PASS")
        finally:
            process.terminate()
            process.wait(timeout=5)
            mock.shutdown()
            mock.server_close()
if __name__ == "__main__":
    main()
