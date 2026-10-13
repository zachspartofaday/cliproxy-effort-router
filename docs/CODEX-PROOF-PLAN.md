# Native Codex proof: approval proposal, not authorization

**No credentialed test has run. Do not install on whiterock until this proof passes.** Fleet reports the host hold is cleared; that does not clear this project's Codex gate. Zach must approve this plan through Fleet before setup/login/inference. Darwin binaries below are disposable test scaffolding for the Linux CPA plugin.

## Meaning of “works on Codex”

For **Codex CLI 0.162.1 and T3's provider driving that same binary**, GPT-6.1-Sol, code mode, single agent, native Responses:

1. Active routing applies a classifier decision to each eligible new user turn using `configuration_update`, with the root `reasoning.effort` held at the client's configured baseline (high). At least two different effective choices must be exercised. Explicit nonbaseline client overrides remain authoritative. Tool round trips and retries reuse the decision; they do not classify again or move old updates.
2. Both standalone `POST /v1/responses/compact` and the client's `/compact` succeed. The pinned client implements streaming compaction with `compaction_trigger`; this is distinct from the standalone compact endpoint. Standalone compact receives no plugin inserts. A returned opaque compaction item starts a new context window, with a fresh effort update before the next new user turn.
3. Within each context window, previously submitted input items and stable top-level prompt fields remain identical across tool round trips and appended turns, including turns selecting a different effort. Actual provider cache counters show positive hits on warmed prefixes across an effort change. Compaction intentionally establishes a new prefix; compare cache behavior again after warming that new window. Missing cache counters, persistent cold misses, or missing diagnostic records leave this requirement **unproven**.
4. Shadow leaves bodies byte-identical and still records classifications. Active inserts/replays correctly. Jev failures/timeouts fail open without introducing a later historical change. Restart replay is stable. Claude-format regression checks continue to pass.
5. Both CLI and T3 complete the above with code-mode tool calls. No silent fallback to another model, chat-completions path, non-code mode, or another client version is accepted.

OpenAI documents mid-conversation updates for GPT-6 **single-agent** sessions: fixed root effort, original update positions, no adjacent updates, no automatic compaction/truncation, and standalone compact incompatibility. See [official reasoning guide](https://developers.openai.com/api/docs/guides/reasoning#change-reasoning-mid-conversation). `response.reasoning.effort` remains the root effort; it is **not proof of the selected per-turn effort**. This plugin skips automatic context management and truncation=auto. Multi-agent sessions are outside the qualified configuration.

## Completed offline evidence

- Source pins: CPA 8.0.26 `a27799ec2b0cc5a60e49970d5467829ceaab8131`; Codex rust-v0.162.1 `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`. [Synthetic shapes and source references](../testdata/codex-0.162.1/README.md) contain no captured prompts or real opaque blobs.
- Go unit/race tests cover native turn detection, stable replay, adjacent baseline client updates, explicit overrides, compaction windows, shadow/errors, sessions and Claude behavior. ABI smoke exercises the C interface.
- `scripts/cpa-mock-proof.py` runs the **official CPA 8.0.26 Darwin/arm64 binary**, plugin and adapter with temporary empty auth directories, fake keys and localhost-only upstreams. Disabled baseline, shadow and active all pass native sampling/tool loops, standalone/stream compaction, a new window and Claude. [Recorded evidence](CODEX-MOCK-EVIDENCE.json) includes redacted upstream item shapes and normalizer HMAC equality observations.
- Official Darwin archive SHA-256: `525040a0c901a4e6174ba604b852b96de111b54be5325942a99c00c2c4593b36`, checked against the [official release checksum manifest](https://github.com/router-for-me/CLIProxyAPI/releases/tag/v8.0.26). Its binary is `/tmp/effort-router-cpa-binary/cli-proxy-api` in this session; re-fetch and verify if absent. No existing Mac CPA/Codex/Claude auth/config was inspected.
- Mock cached_tokens=1200 is explicitly labelled simulated. This proves plumbing, **not actual Codex execution, provider acceptance, latency or cache hits**. Linux library runtime and the real T3 path are also pending.

## Ownership and secret boundary

Exact protected prefix:

`/Users/zachskjaveland/Library/Application Support/effort-router-proof/private/`

There is **no enforcing Fleet guard on the Mac**. Zach creates the prefix himself: directories 0700, files 0600, owned by his user. Only commands Zach runs provision real credentials. CPA and the adapter may read those credentials during the approved run; agents may pass paths but must never read, print, provision, modify or delete their contents. Same-user permissions are accidental-disclosure protection, not a hard boundary against an agent running as Zach.

Repo [.claude/settings.json](../.claude/settings.json) denies Read/Edit/Grep on this absolute prefix. This is a belt, not an OS sandbox or protection against arbitrary subprocesses. **A Codex session must never `cat`, `grep`, `head`, `jq`, read, edit or search anything under the prefix, including through scripts, symlinks, sudo or subprocess workarounds.** The operator-only setup/export commands below must not be executed by an agent. No credential is put in chat, argv, ordinary environment, git, a public temp file, or a captured agent transcript.

CPA auth, Zen key, isolated client homes, all raw CPA/adapter/relay/client process/error logs and any failed-request dumps stay under that prefix. `request-log:false` and `debug:false` alone do not guarantee CPA never writes error bodies: CPA's working directory and log destinations are private too. The proof workspace contains only newly authored synthetic text. Agents inspect **only the allowlisted export outside the prefix**. Normalizer HMAC salt is random, memory-only and never exported; only whitelisted enums, HMAC scope, equality booleans, statuses and numeric usage leave.

## Accounts and bounded usage to approve

Recommend one Zach-owned **ChatGPT/Codex subscription account** eligible for GPT-6.1-Sol, freshly logged into the throwaway CPA; one proof-only Zen key in Zach's Zen account for `jev-1.13-free`; and, for four real Claude compatibility calls, one Zach-owned eligible Claude subscription account freshly logged into the same throwaway CPA auth directory. No existing Mac/whiterock auth files are copied or read. No new paid OpenAI/Anthropic API key is requested. If subscription/model access is unavailable, stop and report it rather than substituting paid access or another model.

Recommend a 45-minute run: **at most 40 GPT-6.1-Sol upstream requests including tools/compaction, 4 Claude requests, and 40 Jev classifications**, stop at 20,000 observed output tokens (including reasoning where included). Expect roughly 24–36 Codex requests, 5,000–15,000 output/reasoning tokens and 0.5–2 million logical input tokens, many cached; these are planning estimates, not measured usage or a quota guarantee. The relay enforces request/output caps per process; Zach must count totals across restarts/modes and stop at the overall cap. Jev is advertised as limited-time free in [Zen's documentation](https://opencode.ai/docs/en/zen/); verify that status in Zach's account before running, and stop if it is no longer free unless separately approved. No OCG/DeepSeek credentialed comparison is in this Mac budget.

Jev plus DeepSeek **paired shadow is already implemented and mock-tested**. Recommend Jev alone for this gate; after native proof/deployment approval, use existing whiterock OCG routing for a paired shadow week. DeepSeek needs the separately protected, **unscoped** CPA key and cannot replace Jev on failure. Testing it on the Mac would need additional protected OCG provisioning and separate approval, never copying whiterock secrets.

## Exact operator setup, only after Fleet relays approval

1. Build disposable test scaffolding from the reviewed commit, and verify the CPA binary above. No credentials are involved in this build:

   ```sh
   go test ./...
   go vet ./...
   go build -buildmode=c-shared -o /tmp/effort-router-proof.dylib .
   go build -o /tmp/jev-ocg-adapter-proof ./adapter
   codex --version  # must be 0.162.1; version check does not inspect auth
   ```

   The installed Mac CLI version is not yet established. Zach must supply/install exactly 0.162.1 if different. Do not read his existing Codex home to infer it. Keep receipt of binary version and source/artifact checksums in the nonsecret report.

2. **Zach's own terminal**, outside any agent capture, creates and populates the private tree. Commands assume this checkout is the working directory:

   ```sh
   umask 077
   proof_private='/Users/zachskjaveland/Library/Application Support/effort-router-proof/private'
   mkdir -p "$proof_private" "$proof_private/cpa-auth" "$proof_private/plugins" \
     "$proof_private/codex-home" "$proof_private/claude-home" "$proof_private/logs"
   chmod 0700 "$proof_private" "$proof_private/cpa-auth" "$proof_private/plugins" \
     "$proof_private/codex-home" "$proof_private/claude-home" "$proof_private/logs"
   cp /tmp/effort-router-proof.dylib "$proof_private/plugins/effort-router.dylib"
   cp deploy/mac-proof/codex-config.toml "$proof_private/codex-home/config.toml"
   python3 - "$proof_private" deploy/mac-proof/cpa-config.json <<'PY'
   import json, pathlib, sys
   private = pathlib.Path(sys.argv[1])
   template = pathlib.Path(sys.argv[2]).read_text()
   config = json.loads(template.replace('REPLACE_WITH_PRIVATE_PREFIX', str(private)))
   (private / 'cpa-config.yaml').write_text(json.dumps(config, indent=2))
   PY
   ```

   JSON is valid YAML for CPA. This isolated CPA listens on 127.0.0.1:8318; the numeric-usage relay listens on 127.0.0.1:8319; adapter on 127.0.0.1:8766. Check these ports are free without probing any existing service's credentials/config. Test CPA has **no client access keys** and is local only; its process lifetime is short and any local user can invoke it while running. No adapter CPA credential is needed for Jev-only. If that local-user exposure is unacceptable, stop and revise the plan before login; don't introduce an agent-visible key.

3. **Zach performs OAuth himself**, in that same private working directory. CPA's pinned source confirms these flags:

   ```sh
   cd "$proof_private"
   /tmp/effort-router-cpa-binary/cli-proxy-api -config "$proof_private/cpa-config.yaml" -codex-login -no-browser
   /tmp/effort-router-cpa-binary/cli-proxy-api -config "$proof_private/cpa-config.yaml" -claude-login -no-browser
   ```

   Open the displayed authorization link yourself and select the approved account. Login output/browser state is operator-private; never paste it back. Auth is written by CPA to `cpa-auth`, under umask 077. The second command is only for the approved Claude portion. Do not use/import any preexisting auth file. Report only success/failure and the nonsecret model availability. OAuth needs a local redirect listener; a occupied callback port is a stop condition, not reason to attach to another running CPA.

4. **Zach creates the Zen credential with no echo**, not in an agent session:

   ```sh
   python3 - "$proof_private/zen-key" <<'PY'
   import getpass, os, sys
   descriptor = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
   with os.fdopen(descriptor, 'w') as output:
       output.write(getpass.getpass('Proof-only Zen key (hidden): ') + '\n')
   PY
   ```

   The credential path is the only key-related value agents may receive. Do not ask an agent to run this command. The adapter reads this file, strips whitespace and never logs its contents.

5. Start processes from Zach's private terminal(s), under umask 077 and with working directory set to the private prefix; use absolute repo path for the relay. **Do not start them in an agent terminal.** Each process's stdout/stderr goes to private logs:

   ```sh
   cd "$proof_private"
   /tmp/effort-router-cpa-binary/cli-proxy-api -config "$proof_private/cpa-config.yaml" \
     >"$proof_private/logs/cpa.log" 2>&1
   # In another operator terminal, after defining proof_private and umask 077:
   /tmp/jev-ocg-adapter-proof -listen 127.0.0.1:8766 -backend jev \
     -jev-model jev-1.13-free -zen-credential-file "$proof_private/zen-key" \
     >"$proof_private/logs/adapter.log" 2>&1
   # Third operator terminal:
   python3 /Users/zachskjaveland/Coding/cliproxy-effort-router/scripts/proof-usage-relay.py \
     --mode shadow --output "$proof_private/usage-proof.jsonl" \
     >"$proof_private/logs/relay.log" 2>&1
   ```

   Commands are foreground to make ownership/stopping clear. Adapter and relay also run from the private directory. Health checks may report status only. Request bodies/error logs are never used for diagnosis by an agent; Zach can report fixed-category/status outcomes. Missing/invalid proof records block acceptance.

6. Create a fresh **synthetic-only** workspace outside the private prefix, with a stable README of at least 1024 tokens, a tiny code file and tasks such as acknowledge, count lines with code-mode tools, and propose a small algorithm/test. No genuine project files or secrets. In Zach's terminal launch:

   ```sh
   CODEX_HOME="$proof_private/codex-home" codex -C /tmp/effort-router-synthetic-workspace
   ```

   `CODEX_HOME` is a child-only isolation setting, never a reassignment of HOME. The supplied config pins model/provider/baseline/code_mode/single-agent and HTTP Responses, disables WebSockets and transport retries, and avoids direct OpenAI auth. All Codex session files stay in the isolated home. Interactive display contains synthetic prompts only; any saved terminal/process/error transcript belongs under private/logs. Do not enable body/debug traces. Check actual code-mode tool-item shapes in the exported evidence, not just the feature toggle.

7. Fleet/Zach configure a **new Mac T3 test provider/thread**, using that same 0.162.1 binary and isolated `CODEX_HOME`/config/workspace, without changing the normal provider or using whiterock. A launch wrapper may set the child environment and redirect **stderr only** to private logs; stdout is the app-server protocol and must remain connected to T3. Disable T3 raw protocol/process logging or direct its raw logs to this private prefix before running. Synthetic conversation storage is expected; do not copy raw protocol/error logs into a report. If T3 cannot isolate the environment or its raw logs, stop this leg and report that unresolved gate. Fleet knows T3's provider UI; an agent must not discover setup by reading existing auth/config. Preserve a version/model/code-mode receipt and repeat the sampling/compaction checks below. An HTTP proof qualifies the pinned HTTP setup; if production uses WebSockets, also test that transport under an amended approved plan before claiming production qualification.

## Run sequence and evidence

1. Start with **shadow**, fresh conversations in CLI and T3. Each uses two small turns and a code-mode tool round trip. Verify exported `changed:false`, no effort updates added, classification count/latency outcomes and numeric usage. Try client `/compact`, then a new user turn. Run two synthetic Claude Messages requests (medium baseline, one ordinary and one tool/history case) through relay; no changes in shadow.
2. Zach stops CPA and relay, changes only `shadow:false` and the state path to a new private `active.jsonl` in the private config, restarts them with `--mode active`. Keep the same `normalizer-proof.jsonl` and append usage file. Use fresh CLI/T3 sessions. Complete two turns with differing effort choices, tool continuation, and a repeated/restarted session replay. Run client `/compact`, a post-compaction turn, and a following warm-prefix turn. Inspect only the allowlisted export. Explicit override checks use a separate session because changing the client's root baseline legitimately changes its prefix.
3. Jev decides normally. If the corpus selects only one effort, use a separately labelled **local deterministic classifier** low→high→medium for the protocol/cache test, then return to actual Jev for the end-to-end leg. Never call those deterministic picks Jev accuracy evidence. The existing mock can provide deterministic choices, but mixing test sources must appear in the report. If Jev times out at 1200 ms, collect fixed outcomes/latency; do not silently increase plugin timeout or switch paid models. A proposed revised latency budget requires Zach's approval.
4. Zach submits a synthetic standalone compact request to relay `/v1/responses/compact` using the public fixture, in his private terminal:

   ```sh
   curl --silent --show-error --fail \
     -H 'Content-Type: application/json' \
     --data-binary @/Users/zachskjaveland/Coding/cliproxy-effort-router/testdata/codex-0.162.1/standalone-compact.json \
     http://127.0.0.1:8319/v1/responses/compact \
     >"$proof_private/logs/standalone-compact.json" 2>"$proof_private/logs/standalone-compact.err"
   ```

   Zach checks the private response himself and reports only status=200/presence of compaction output. The relay exports status/numeric usage. A code-mode `/compact` success is separate evidence and must include a subsequent successful turn.
5. Complete the remaining two real Claude synthetic calls in active mode. Existing unit/mock tests cover directive/manual effort/tool history and Claude-format GPT-6 translation; four live calls check actual Claude acceptance before/after shadow. If Claude account access is not approved, mark live Claude acceptance pending rather than declaring a full pass from mock evidence.
6. Warm the same prompt prefix and observe **positive actual cached_tokens** on native turns choosing different effort and continuing tools. Compare normalizer HMAC prefix/stable-top-field booleans, per-request top fields unchanged, original item positions and updates around compaction. Reusing the original serialized prefix is necessary; token counters show whether the provider actually cached it. Low token counts below the cache threshold are inconclusive. No cached_tokens exposed => unresolved cache proof. Numeric counters are responses received, not billing/complete usage for cancelled requests.

`proof-file` is optional and absent from production config. It captures **normalizer-stage** before/after shapes and in-memory HMAC comparisons; it is not an upstream acknowledgement. The mock proof also checks actual mock upstream payloads after CPA thinking/transformation. Live upstream HTTP success, actual client completion, update shape positions and provider usage must accompany the normalizer evidence. Diagnostics fail independently of routing; a missing observation cannot be counted as a pass.

`scripts/proof-usage-relay.py` streams responses to clients and exports only endpoint/mode/status and numeric token counters, never IDs, headers, bodies or upstream error strings. It allows only the required local endpoints and uses no proxy environment. It does not implement WebSockets. `scripts/proof-tools-test.py` checks numeric filtering and exporter rejection with synthetic secret-shaped strings. All private files remain 0600 under the operator umask.

## Export and cleanup, owned by Zach

After stopping traffic, Zach runs the allowlist exporter himself. It reads only the two named proof files, rejects unknown fields/string values, and exclusively creates a new output outside private. Agents never inspect even the nominally safe files **inside** private:

```sh
umask 077
python3 /Users/zachskjaveland/Coding/cliproxy-effort-router/scripts/proof-export.py \
  --private '/Users/zachskjaveland/Library/Application Support/effort-router-proof/private' \
  --output /tmp/effort-router-approved-proof.json
```

Only that HMAC-redacted proof file (including numeric usage/statuses) and a short operator attestation of versions, accounts-by-provider-only, code mode, CLI/T3 `/compact` completion and approved limits leave. No screenshot/terminal recording containing OAuth links, keys, prompts or raw errors. Review evidence for CLI/T3 separately; absence of client identity in diagnostic output means Zach must supply the ordered synthetic-run mapping, with no private session IDs. Full pass/fail report includes the source commit and binary checksums.

Zach stops CPA, adapter, relay and test clients, removes/disables the T3 test provider, revokes the proof-only Zen key and **logs out/revokes the throwaway CPA Codex OAuth session if supported** (and Claude authorization). CPA 8.0.26's CLI has no verified logout flag; use the provider/account's authorized-session controls if available. Deleting local auth is not proof of server-side revocation. Finally, Zach deletes the **whole** private prefix with a command he runs himself. Agents do not clean it up. Retain only the approved redacted export and nonsecret artifact receipts.

## Decision requested from Zach

Approve the bounded Mac proof above, one subscription account per Codex/Claude and a proof-only free Jev Zen key, including isolated T3 setup. Recommended answer: **approve, with the stated 40/4/40 cap; Jev first; no whiterock install until CLI and T3 native/cache/compaction gates pass.** Zach alone supplies login/Zen provisioning and decides whether unavailable cache/Claude/T3 evidence merits an explicitly changed gate. The recommendation is to leave those gates unresolved until evidence exists, not waive them.
