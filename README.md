# effort-router

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) request-normalizer plugin. For each new user turn it asks a Jev-style classifier how much reasoning effort the turn needs, then sets that effort in a way that keeps the prompt cache intact.

| Upstream | How effort is set | Live status (reference deployment, 2026-10-17) |
|---|---|---|
| Claude (`claude-fable-5-1`, `claude-opus-5-5`) | effort-only system message `{"role":"system","content":[],"output_config":{"effort":…}}`, placed after Claude Code's own per-turn effort message | inserted message seen in CPA's upstream request; how Anthropic weighs it against the top-level effort is unverified; cache hit rate unchanged so far |
| GPT-6 (`gpt-6*` via Codex, native Responses and Claude-format clients) | `{"type":"configuration_update","reasoning":{"effort":…}}` input item before the user turn | forwarded effort observed changing (high to low/medium, and up to xhigh); cache hit rate within the day's normal range so far |
| GPT-5.x | not routed: an effort change there rewrites the hidden system prefix | — |

On this fork, `shadow: true` records choices without inserting anything. Start with [the security review](docs/REVIEW.md). The loopback Go adapter under `adapter/` uses actual Zen Jev by default; optional parallel DeepSeek comparison requires explicit plugin shadow mode and logs both picks/latencies without prompt text.

## Status

The reference deployment runs this build in production CPA in **active mode**, by the owner's choice, ahead of the native Codex proof gate below; it is live but not qualified. Shadow ran first (2026-10-17, about 240 classifications an hour, p95 under 500 ms). To roll back, set `shadow: true` in the plugin config: CPA hot-reloads it and request bodies pass through unchanged. Stopping the adapter only stops new classifications; turns already decided keep replaying their effort inserts, so histories stay stable, until shadow is on.

## Codex support

Native Codex sends a fixed `reasoning.effort` on every request, so it has its own baseline: `codex-baseline-effort`. A request whose `reasoning.effort` differs from it is a manual choice and is left alone. Claude-format requests to GPT-6 models compare against `baseline-effort` instead. Set both to the effort your clients normally run at; the reference deployment uses `high` for both, so `xhigh` sessions are never touched and the router can move a turn down to `low`/`medium` or up to `xhigh`.

The router may also raise effort: a turn classified `xhigh` in a `high` session is sent at `xhigh`.

To see what was forwarded without reading bodies, compare the plugin's state file with CPA's usage log: for Codex the logged `reasoning_effort` reflects the update and matches the router's decision to the second. For Claude the usage log records only the top-level effort, which the router never changes, so it always shows the client's value. Confirming Claude needs CPA's request log, where `API REQUEST` holds the forwarded body.

Each turn is classified once, on its first request, and the decision is stored in an append-only JSONL file. Later requests replay the same inserts at the same positions, so history stays append-only.

If you set `/effort` yourself to a non-baseline value, your setting wins. If the classifier fails or times out, that turn gets no insert at all. Decisions must persist before they are applied. Logs contain hashes, choices and fixed error categories, with no prompt previews. Shadow and active decisions use separate key namespaces, so switching to active never replays shadow decisions into existing histories, even in the same state file; a separate active state file only keeps the evidence apart.

## Classifier

Any server that implements TypeSafe's Jev contract works: `POST /v1/systemone` with model `jev-latest` and a `choice` question, returning `answers.effort.choice`. SemIf, Laya and Jev all qualify.

Pick the service with `jev-provider` (a dropdown in the Management Center):

| `jev-provider` | Endpoint | Key |
|---|---|---|
| `custom` (default) | `jev-url`, such as the bundled loopback adapter, SemIf or Laya | `jev-api-key` if the server needs one |
| `typesafe` | `https://api.typesafe.ai/v1/systemone` | TypeSafe console key |
| `opencode` | `https://opencode.ai/inference/systemone/v1/systemone`; `jev-latest` is sent as `jev-1.13` | OpenCode Console key |

With `typesafe` or `opencode` the key lives in CPA's config and the internal `turn_id`/`router_shadow` fields are not sent. The bundled adapter keeps the key out of CPA (a systemd credential) and adds an optional DeepSeek comparison, so the reference deployment uses `custom` pointed at it.

The bundled adapter calls OpenCode Zen at `https://opencode.ai/inference/systemone/v1/systemone`; the legacy `/zen/v1` path throttles free Jev with HTTP 429. Free `jev-1.13-free` also rate-limits at production volume (100% 429 after about an hour of ~240 calls/h), so run paid `jev-1.13` (`-jev-model jev-1.13`): $0.042 per million input tokens, output free, about 750 input tokens per classification, roughly $0.10/day at that volume. Jev 1.14 is priced the same and adds nothing this router uses. With a non-root `User=`, the adapter accepts systemd's `LoadCredential` copy (root-owned, mode 0440 through an ACL that grants only the service user).

## Install

From the CLIProxyAPI Management Center's plugin store, or download the zip for your platform from [Releases](https://github.com/alex4o/cliproxy-effort-router/releases) and put the library in `~/.cli-proxy-api/plugins/`. All settings below show up in the Management Center's plugin config.

## Build from source

```sh
scripts/build-linux-amd64.sh 0.0.0-shadow
# Produces dist/effort-router.so plus the Linux gateway and SHA256SUMS.
```

The reference deployment installed this ahead of the native Codex proof at its owner's direction; see Status.

## Config (`plugins.configs.effort-router`)

```yaml
effort-router:
  enabled: true
  jev-provider: "custom"    # custom (jev-url) | typesafe | opencode
  jev-url: "http://127.0.0.1:8765/v1/systemone"  # custom only
  jev-model: "jev-latest"   # opencode maps jev-latest to jev-1.13
  # jev-api-key: ...        # required for typesafe and opencode
  shadow: true              # classify/log only; returns identical request bytes
  timeout-ms: 1500
  models: ["claude-fable-5-1", "claude-opus-5-5", "gpt-6"]  # prefixes
  codex-baseline-effort: "high"  # native Codex's usual effort; other values = manual choice
  # proof-file: /absolute/private/path/proof.jsonl  # optional redacted evidence
  baseline-effort: "medium"   # your Claude default (reference deployment: high); other values = manual /effort
  # state-file: /absolute/private/path/effort-router.jsonl
```

## Caveats

- Deleting the state file changes past turns once: a cache miss, and on Fable 5.1 the thinking blocks are invalidated.
- Subagents on routed models get routed too.
- Native sampling routes through CPA's codex format; standalone `/responses/compact` uses openai-response and stays untouched. Streamed compaction_trigger requests replay prior decisions without classifying compaction; opaque compaction anchors start a new window.
- Native automatic truncation/context_management requests stay untouched because OpenAI does not support configuration_update in those modes. Client nonbaseline effort/updates win.

## Fork validation and Linux build

```sh
go test ./...
go test -race ./...
go vet ./...
# Mac cross-build with Go 1.26.9 and Zig 0.17.0:
scripts/build-linux-amd64.sh
```

The gateway reads a private `zen-key` systemd credential (plus `cpa-key` only for OCG comparison); it never loads existing CPA, Claude or Codex credential files. Native Codex and Claude paths have offline tests and actual CPA/mock-upstream tests; real OpenAI cache/compaction and CLI/IDE proof remain required for qualification. See the runbook for ABI smoke tests, checksums, permissions, loopback namespace forwarding and rollback.

Qualification still needs the native Codex 0.162.1 / GPT-6.1-Sol code-mode proof, including compaction, cache behavior and Claude regression. Offline tests and live observation alone do not qualify it.
