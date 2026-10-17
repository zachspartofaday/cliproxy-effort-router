#!/usr/bin/env python3
"""Run by the operator, after approval: allowlist private proof records for export.

Reads only normalizer-proof.jsonl and usage-proof.jsonl, never auth or raw logs.
Rejects unknown fields and strings. Failure prints a fixed error, never content.
"""
import argparse
import json
import pathlib
import re
import sys

FORMATS = {"claude", "codex", "openai-response", "openai", "other"}
MODELS = {"gpt-6.1-sol", "claude-opus-5-5"}
TYPES = {"message", "configuration_update", "function_call", "function_call_output",
         "custom_tool_call", "custom_tool_call_output", "reasoning", "compaction",
         "context_compaction", "compaction_trigger", "other"}
NUMERIC = {"input_tokens", "output_tokens", "cached_tokens", "reasoning_tokens", "cache_read_input_tokens"}

def check(condition):
    if not condition:
        raise ValueError("invalid proof schema")

def normalizer(record):
    required = {"sequence", "from", "to", "model_prefix", "shadow", "scope", "changed",
                "new_window", "request_top_fields_unchanged", "before", "after"}
    optional = {"prefix_preserved", "stable_top_fields_preserved"}
    check(isinstance(record, dict) and required <= record.keys() and record.keys() <= required | optional)
    check(type(record["sequence"]) is int and 0 < record["sequence"] < 2**63)
    check(record["from"] in FORMATS and record["to"] in FORMATS and record["model_prefix"] in MODELS)
    check(isinstance(record["scope"], str) and re.fullmatch(r"[0-9a-f]{64}", record["scope"]))
    for field in {"shadow", "changed", "new_window", "request_top_fields_unchanged"} | optional:
        if field in record:
            check(type(record[field]) is bool)
    for field in ("before", "after"):
        check(isinstance(record[field], list) and len(record[field]) <= 4096)
        for item in record[field]:
            check(isinstance(item, dict) and "type" in item and item.keys() <= {"type", "role", "effort", "payload"})
            check(item["type"] in TYPES)
            check(item.get("role", "") in {"", "user", "assistant", "system", "developer"})
            check(item.get("effort", "") in {"", "low", "medium", "high", "xhigh"})
            check(item.get("payload", "[REDACTED]") == "[REDACTED]")
    return record

def usage(record):
    check(isinstance(record, dict) and record.keys() == {"mode", "endpoint", "status", "usage", "usage_available"})
    check(record["mode"] in {"shadow", "active"})
    check(record["endpoint"] in {"/v1/models", "/v1/responses", "/v1/responses/compact", "/v1/messages"})
    check(type(record["status"]) is int and (record["status"] == 0 or 100 <= record["status"] <= 599))
    check(type(record["usage_available"]) is bool)
    check(isinstance(record["usage"], dict) and record["usage"].keys() <= NUMERIC)
    for value in record["usage"].values():
        check(type(value) is int and 0 <= value < 2**63)
    check(record["usage_available"] == bool(record["usage"]))
    return record

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--private", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    try:
        check(not args.output.resolve().is_relative_to(args.private.resolve()))
        data = {}
        for kind, filename, validate in (("normalizer", "normalizer-proof.jsonl", normalizer),
                                         ("usage", "usage-proof.jsonl", usage)):
            with (args.private / filename).open("rb") as source:
                raw = source.read(32 * 1024 * 1024 + 1)
            check(len(raw) <= 32 * 1024 * 1024)
            data[kind] = [validate(json.loads(line)) for line in raw.splitlines() if line]
            check(data[kind])
        # Exclusive creation avoids following an existing destination symlink.
        with args.output.open("x", encoding="utf-8") as output:
            output.write(json.dumps(data, indent=2) + "\n")
    except Exception:
        print("Proof export rejected; no private content printed.", file=sys.stderr)
        return 1
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
