# effort-router

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) request-normalizer plugin. For each new user turn it asks a Jev-style classifier how much reasoning effort the turn needs, then sets that effort in a way that keeps the prompt cache intact.

| Upstream | How effort is set | Cache |
|---|---|---|
| Claude (`claude-fable-5-1`, `claude-opus-5-5`) | effort-only system message `{"role":"system","content":[],"output_config":{"effort":…}}` | replay tested; live proof pending |
| GPT-6 (`gpt-6*` via Codex, native Responses and Claude-format clients) | `{"type":"configuration_update","reasoning":{"effort":…}}` input item before the user turn | replay tested; live proof pending |
| GPT-5.x | not routed: an effort change there rewrites the hidden system prefix | — |

On this fork, `shadow: true` records choices without inserting anything. Start with [the security review](docs/REVIEW.md) and [the whiterock design/runbook](docs/WHITEROCK-DESIGN.md). The loopback Go adapter under `adapter/` uses actual Zen Jev by default; optional parallel DeepSeek comparison requires explicit plugin shadow mode and logs both picks/latencies without prompt text.

Each turn is classified once, on its first request, and the decision is stored in an append-only JSONL file. Later requests replay the same inserts at the same positions, so history stays append-only.

If you set `/effort` yourself to a non-baseline value, your setting wins. If the classifier fails or times out, that turn gets no insert at all. Decisions must persist before they are applied. Logs contain hashes, choices and fixed error categories, with no prompt previews. Shadow and active decisions are isolated; enabling needs fresh sessions and a separate active state file.

## Classifier

Any server that implements TypeSafe's Jev contract works: `POST /v1/systemone` with model `jev-latest` and a `choice` question, returning `answers.effort.choice`. SemIf, Laya and Jev all qualify.

## Install

From the CLIProxyAPI Management Center's plugin store, or download the zip for your platform from [Releases](https://github.com/alex4o/cliproxy-effort-router/releases) and put the library in `~/.cli-proxy-api/plugins/`. All settings below show up in the Management Center's plugin config.

## Build from source

```sh
scripts/build-linux-amd64.sh 0.0.0-whiterock-shadow
# Produces dist/effort-router.so plus the Linux gateway and SHA256SUMS.
```

Whiterock installation waits for the [native Codex proof plan](docs/CODEX-PROOF-PLAN.md) to pass and Zach's deployment approval.

## Config (`plugins.configs.effort-router`)

```yaml
effort-router:
  enabled: true
  jev-url: "http://127.0.0.1:8765/v1/systemone"
  jev-model: "jev-latest"
  # jev-api-key: ...
  shadow: true              # classify/log only; returns identical request bytes
  timeout-ms: 1500
  models: ["claude-fable-5-1", "claude-opus-5-5", "gpt-6"]  # prefixes
  codex-baseline-effort: "high"  # match native Codex's fixed request effort
  # proof-file: /absolute/private/path/proof.jsonl  # optional redacted evidence
  baseline-effort: "medium"   # Claude Code default; other values = manual /effort
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

The gateway reads a private `zen-key` systemd credential (plus `cpa-key` only for OCG comparison); it never loads existing CPA, Claude or Codex credential files. Native Codex and Claude paths have offline tests and actual CPA/mock-upstream tests; real OpenAI cache/compaction and CLI/T3 proof remain required before installation. See the runbook for ABI smoke tests, checksums, permissions, loopback namespace forwarding and rollback.

The install gate is native Codex 0.162.1 / GPT-6.1-Sol code-mode proof off whiterock, including compaction, cache behavior and Claude regression. Offline tests alone do not authorize installation.
