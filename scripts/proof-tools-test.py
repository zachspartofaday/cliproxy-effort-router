#!/usr/bin/env python3
"""Synthetic, credential-free privacy checks for the proposed operator tools."""
import importlib.util
import pathlib
import unittest

def module(filename):
    path = pathlib.Path(__file__).with_name(filename)
    spec = importlib.util.spec_from_file_location(filename, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

relay = module("proof-usage-relay.py")
export = module("proof-export.py")

class ProofTools(unittest.TestCase):
    def test_usage_drops_payload_and_untrusted_fields(self):
        self.assertEqual(relay.usage_record({"output": "synthetic private content", "usage": {
            "input_tokens": 2048, "output_tokens": 10, "secret": "synthetic private content",
            "input_tokens_details": {"cached_tokens": 1024},
            "output_tokens_details": {"reasoning_tokens": "synthetic private content"}}}),
            {"input_tokens": 2048, "output_tokens": 10, "cached_tokens": 1024})
        self.assertIsNone(relay.counter(True))
        self.assertIsNone(relay.counter(-1))

    def test_export_rejects_unknown_fields_and_strings(self):
        safe = {"mode": "active", "endpoint": "/v1/responses", "status": 200,
                "usage_available": True, "usage": {"cached_tokens": 1024}}
        self.assertEqual(export.usage(safe), safe)
        with self.assertRaises(ValueError):
            export.usage(dict(safe, prompt="synthetic private content"))
        with self.assertRaises(ValueError):
            export.usage(dict(safe, usage={"cached_tokens": "synthetic private content"}))

    def test_normalizer_export_requires_redaction(self):
        safe = {"sequence": 1, "from": "openai-response", "to": "codex",
                "model_prefix": "gpt-6.1-sol", "shadow": False, "scope": "0" * 64,
                "changed": True, "new_window": True, "request_top_fields_unchanged": True,
                "before": [{"type": "message", "role": "user", "payload": "[REDACTED]"}],
                "after": [{"type": "configuration_update", "effort": "low"}]}
        self.assertEqual(export.normalizer(safe), safe)
        with self.assertRaises(ValueError):
            export.normalizer(dict(safe, before=[{"type": "message", "payload": "synthetic private content"}]))

if __name__ == "__main__":
    unittest.main()
