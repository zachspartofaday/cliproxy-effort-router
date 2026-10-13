#!/usr/bin/env python3
"""Black-box CPA 8.0.26 + plugin + Jev gateway with localhost-only fakes.
No existing config/auth files are used. Usage:
  python3 scripts/cpa-mock-proof.py CPA_BINARY PLUGIN_LIBRARY ADAPTER_BINARY
"""
import argparse
import copy
import http.server
import json
import os
import pathlib
import runpy
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "testdata/codex-0.162.1"
validate_proof = runpy.run_path(str(ROOT / "scripts/proof-export.py"))["normalizer"]
records, classifier_calls = [], []
claude_baseline = None

class Mock(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        path = urllib.parse.urlsplit(self.path).path
        if path == "/v1/systemone":
            assert self.headers["Authorization"] == "Bearer fake-zen-proof"
            classifier_calls.append(body["state"]["user_message"])
            effort = {"first synthetic turn": "low", "second synthetic turn": "xhigh", "after compact synthetic turn": "medium", "synthetic Claude turn": "high"}[body["state"]["user_message"]]
            self.send_json({"answers": {"effort": {"choice": effort}}})
            return
        records.append((self.path, body))
        if path == "/responses/compact":
            assert not any(item.get("type") == "configuration_update" for item in body["input"])
            self.send_json({"id": "cmp_mock", "object": "response.compaction", "output": [{"type": "compaction", "encrypted_content": "SYNTHETIC_NOT_ENCRYPTED"}], "usage": {"input_tokens": 2000, "output_tokens": 50}})
        elif path == "/responses":
            assert self.headers["Authorization"] == "Bearer fake-upstream-proof"
            output = [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "synthetic reply"}]}]
            if any(item.get("type") == "compaction_trigger" for item in body["input"]):
                output = [{"type": "compaction", "encrypted_content": "SYNTHETIC_NOT_ENCRYPTED"}]
            response = {"id": "resp_mock", "object": "response", "status": "completed", "model": "gpt-6.1-sol", "output": output, "usage": {"input_tokens": 2000, "output_tokens": 20, "input_tokens_details": {"cached_tokens": 1200}}}
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            self.wfile.write(("data: " + json.dumps({"type": "response.completed", "response": response}) + "\n\n").encode())
        elif path == "/v1/messages":
            self.send_json({"id": "msg_mock", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": [{"type": "text", "text": "synthetic reply"}], "stop_reason": "end_turn", "usage": {"input_tokens": 2000, "output_tokens": 20}})
        else:
            self.send_error(404)
    def send_json(self, body):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(body).encode())
    def log_message(self, *args):
        pass

def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]

def request(origin, endpoint, payload=None):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(origin + endpoint, data=data, headers={"Authorization": "Bearer fake-client-proof", "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=5) as response:
        return response.status, response.read()

def efforts(body):
    return [item["reasoning"]["effort"] for item in body["input"] if item.get("type") == "configuration_update"]

def redacted(body):
    # Evidence stores shape only: no prompts, tool arguments, opaque blobs or headers.
    return {"model": body.get("model"), "reasoning": body.get("reasoning"), "input": [{"type": item.get("type", "message"), "role": item.get("role"), **({"effort": item["reasoning"]["effort"]} if item.get("type") == "configuration_update" else {"payload": "[REDACTED]"})} for item in body.get("input", [])]}

def run(cpa, library, adapter, shadow, evidence):
    global claude_baseline
    records.clear();classifier_calls.clear()
    mock = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Mock)
    threading.Thread(target=mock.serve_forever, daemon=True).start()
    processes = []
    with tempfile.TemporaryDirectory(prefix="effort-router-cpa-mock-") as directory:
        root = pathlib.Path(directory)
        (root / "auth").mkdir();(root / "plugins").mkdir()
        (root / "plugins" / "effort-router.dylib").write_bytes(library.read_bytes())
        credential = root / "fake-zen-key";credential.write_text("fake-zen-proof");credential.chmod(0o600)
        cpa_port, adapter_port = port(), port()
        config = {
            "config-version": 8, "server": {"host": "127.0.0.1", "port": cpa_port},
            "oauth": {"auth-dir": str(root / "auth")},
            "access": {"api-keys": ["fake-client-proof"]},
            "management": {"allow-remote": False, "disable-control-panel": True},
            "debug": False, "request-log": False, "logging-to-file": False,
            "disable-image-generation": True,
            "api-keys": {
                "codex": [{"name": "mock-codex", "base-url": f"http://127.0.0.1:{mock.server_port}", "models": [{"name": "gpt-6.1-sol", "alias": "gpt-6.1-sol", "support-configuration-update": True, "thinking": {"levels": ["low", "medium", "high", "xhigh"]}}], "keys": [{"api-key": "fake-upstream-proof", "disable-codex-cloaking": True}]}],
                "claude": [{"name": "mock-claude", "base-url": f"http://127.0.0.1:{mock.server_port}", "models": [{"name": "claude-opus-5-5", "alias": "claude-opus-5-5"}], "keys": [{"api-key": "fake-claude-proof"}]}],
            },
            "plugins": {"enabled": True, "dir": str(root / "plugins"), "configs": {"effort-router": {"enabled": shadow is not None, "shadow": bool(shadow), "models": ["gpt-6.1-sol", "claude-opus-5-5"], "jev-url": f"http://127.0.0.1:{adapter_port}/v1/systemone", "codex-baseline-effort": "high", "state-file": str(root / "state.jsonl"), "proof-file": str(root / "safe-proof.jsonl")}}},
        }
        path = root / "config.yaml";path.write_text(json.dumps(config));path.chmod(0o600)
        # An allowlisted child environment prevents accidental HOME_JWT/store/key
        # inheritance; no existing Mac config/auth directory is used or examined.
        child_env = {"PATH": os.defpath, "TMPDIR": directory}
        with (root / "process-log").open("wb") as output:
            try:
                processes.append(subprocess.Popen([str(adapter), "-listen", f"127.0.0.1:{adapter_port}", "-jev-url", f"http://127.0.0.1:{mock.server_port}/v1/systemone", "-zen-credential-file", str(credential)], cwd=root, env=child_env, stdout=output, stderr=output))
                processes.append(subprocess.Popen([str(cpa), "-config", str(path)], cwd=root, env=child_env, stdout=output, stderr=output))
                origin = f"http://127.0.0.1:{cpa_port}"
                for _ in range(150):
                    try:
                        request(origin, "/v1/models");break
                    except (OSError, urllib.error.HTTPError):
                        if any(p.poll() is not None for p in processes):
                            raise AssertionError("mock-only process exited; inspect its temporary fake-only log while debugging")
                        time.sleep(.02)
                else:
                    raise AssertionError("mock CPA did not become ready")
                mapped = {}
                for name in ["turn-1", "tool-loop", "turn-2", "standalone-compact", "stream-compact", "after-compact"]:
                    before = json.loads((FIXTURES / (name + ".json")).read_text())
                    endpoint = "/v1/responses/compact" if name == "standalone-compact" else "/v1/responses"
                    status, response = request(origin, endpoint, before)
                    assert status == 200 and response, name
                    _, after = records[-1];mapped[name] = copy.deepcopy(after)
                    expected = [] if shadow is not False or name == "standalone-compact" else {"turn-1": ["low"], "tool-loop": ["low"], "turn-2": ["low", "xhigh"], "stream-compact": ["low"], "after-compact": ["medium"]}[name]
                    assert efforts(after) == expected, (name, efforts(after), expected)
                    if shadow is not False or name == "standalone-compact":
                        assert after["input"] == before["input"], "shadow/compact request changed"
                    if name != "standalone-compact":
                        assert after["reasoning"]["effort"] == "high", "baseline changed"
                    evidence.append({"mock_only": True, "shadow": shadow, "case": name, "http_status": status, "before": redacted(before), "after": redacted(after)})
                assert mapped["tool-loop"]["input"][:len(mapped["turn-1"]["input"])] == mapped["turn-1"]["input"]
                assert mapped["turn-2"]["input"][:len(mapped["tool-loop"]["input"])] == mapped["tool-loop"]["input"]
                assert len(classifier_calls) == (0 if shadow is None else 3), "duplicate classification or compact classified"
                claude = {"model": "claude-opus-5-5", "max_tokens": 64, "messages": [{"role": "user", "content": "synthetic Claude turn"}]}
                request(origin, "/v1/messages", claude)
                after = records[-1][1]
                if shadow is None:
                    claude_baseline = copy.deepcopy(after["messages"])
                elif shadow:
                    assert after["messages"] == claude_baseline, "shadow differs from CPA baseline"
                else:
                    assert after["messages"][0]["output_config"]["effort"] == "high"
                evidence.append({"mock_only": True, "shadow": shadow, "case": "claude-regression", "passed": True})
                if shadow is not None:
                    safe = (root / "safe-proof.jsonl").read_text()
                    assert "first synthetic turn" not in safe and "fake-zen-proof" not in safe
                    lines = [json.loads(line) for line in safe.splitlines()]
                    for line in lines:
                        validate_proof(line)
                    sampling = [line for line in lines if line["from"] == "openai-response" and line["to"] == "codex"]
                    assert len(sampling) == 5
                    assert sampling[1]["prefix_preserved"] is True and sampling[2]["prefix_preserved"] is True
                    assert sampling[-1]["new_window"] is True
                    evidence.extend({"mock_only": True, "kind": "normalizer-proof", **line} for line in lines)
            finally:
                for process in reversed(processes):
                    process.terminate()
                for process in reversed(processes):
                    process.wait(timeout=5)
                mock.shutdown();mock.server_close()
    print(f"CPA 8.0.26 mock-only {'disabled baseline' if shadow is None else 'shadow' if shadow else 'active'}: native sampling, tool replay, standalone/stream compaction, new window, Claude PASS")

def main():
    parser = argparse.ArgumentParser();parser.add_argument("cpa", type=pathlib.Path);parser.add_argument("library", type=pathlib.Path);parser.add_argument("adapter", type=pathlib.Path);parser.add_argument("--evidence", type=pathlib.Path)
    args = parser.parse_args();evidence = []
    for shadow in (None, True, False):
        run(args.cpa.resolve(), args.library.resolve(), args.adapter.resolve(), shadow, evidence)
    if args.evidence:
        args.evidence.write_text(json.dumps(evidence, indent=2) + "\n")
if __name__ == "__main__":
    main()
