# Whiterock Jev / OCG design and install runbook

**Status: prepared on the Mac; whiterock remains on HOLD.** Everything below involving whiterock, Incus, CPA config, live inference, or service installation is for Fleet to execute only under Zach's explicit hold exception. No exception has been requested or used by this project session. Fleet owns fleet records; this repository changes neither LimitlessFleet nor LimitlessPlatform.

## Service and classifier

`adapter/` is a small Go HTTP service. It accepts the effort-router subset of Jev at POST `/v1/systemone` (`model: jev-latest`, choice question, four effort values) and returns `answers.effort.choice`. It constructs a fixed rubric and a Responses strict JSON schema with a single enum field; it rejects malformed, oversized, incomplete, refusal and out-of-enum results rather than guessing. No tools, streaming, retries, background jobs or conversation storage are enabled.

Initial model: **`ocg/deepseek-v4.1-flash`**, the fleet's known working cheap worker model. It uses POST `/v1/responses`, classifier reasoning=low and max_output_tokens=256. The fleet verified ordinary Responses requests, not this exact schema/token budget. Confirm structured output survives CPA's selected OCG route before enabling broad shadow collection. CPA's Responses -> Chat translator maps `text.format` to `response_format`; that is source evidence, not proof the live provider honors it. If 256 tokens produces `incomplete` at low reasoning, compare a slightly larger cap under an approved measurement budget. Do not silently accept prose or remove schema validation. Alternatives: `ocg/qwen3.8-flash` and `ocg/glm-5.3-flash`, evaluated with the same contract and rubric.

CPA normalizer requests lack session headers. The plugin now supplies an opaque turn digest; the adapter hashes it into a stable **per-turn** `X-Opencode-Session`. Retries of a turn keep the same ID; different turns normally get different IDs. This satisfies OpenCode's session requirement, permits turn-level affinity and avoids grouping all clients into one classifier session. It sacrifices cross-turn session affinity; the classification request is stateless and fully specified. Identical histories across different conversations still correlate because no conversation identity is exposed by the ABI.

Default deadlines: adapter 1200 ms, plugin 1500 ms; the 300 ms margin covers both local proxy hops and parsing, but plugin state fsync is outside the HTTP deadline. All decisions are synchronous, including in shadow mode. A timed-out/failed/saturated turn is logged with a fixed category and permanently gets no insert. Adapter caps four concurrent calls, plugin eight; neither queues unlimited new calls. Shutdown cancels/drains adapter requests for at most three seconds.

No OCG latency was measured: no credentials were accessed and no live upstream calls were made. Under a separate approved measurement exception, run a small, synthetic nonsecret corpus covering low/medium/high/xhigh, short turns and the maximum 1200+600-rune state. Start sequentially, then use 2 and 4 concurrent calls. Measure end-to-end p50/p95/p99, timeout/busy/incomplete/schema error rates, CPA/OCG session-header acceptance, token counts, and provider billing. Measure actual selected route, not just the model name. Recommended initial acceptance targets: >=99% valid decisions, p95 below 1000 ms and p99 below 1200 ms at expected concurrency; otherwise evaluate the alternatives/free Jev before raising the plugin timeout. These are proposed targets, not observed results.

## Free Jev comparison

Zach noted Jev on OpenCode Zen is free. The public [Zen documentation](https://docs.opencode.ai/docs/zen/) lists `jev-1.13-free` at `https://opencode.ai/zen/v1/systemone`, authenticated with a Zen API key, and describes the free model as limited-time. That endpoint directly speaks this plugin's original Jev contract; it needs no OCG generation adapter. Keep it as a comparison candidate for latency, accuracy and actual cost. It sends the truncated prompt state to Zen/TypeSafe rather than the OCG route and needs a distinct protected credential. CPA 8.0.26's generic model routes do not establish support for forwarding arbitrary `/v1/systemone` requests. Do not assume Zen's Jev can be reached through CPA's Responses endpoint or that the OCG aliases include it. This branch implements the requested OCG path; switching to direct Zen is a separate operator decision/credential setup, with `jev-model: jev-1.13-free` and the documented HTTPS endpoint. Never place a Zen key in agent-readable YAML.

## Placement and credentials

Run the adapter on the **whiterock host**, as dedicated user/group `jev-ocg-adapter`, on `127.0.0.1:8765`. It calls CPA's existing host exposure `127.0.0.1:9444`, forwarded to container CPA `127.0.0.1:8317`. CPA's plugin uses container `127.0.0.1:8765`, forwarded **back to host** loopback by a new non-NAT Incus proxy device with `bind=instance`. Host loopback and container loopback are different namespaces. [Incus proxy documentation](https://linuxcontainers.org/incus/docs/main/reference/devices_proxy/) supports this reverse direction and `bind=instance`; confirm it against the installed Incus and container before traffic. Do not bind either listener to 0.0.0.0 or a LAN address.

`deploy/jev-ocg-adapter.service` uses systemd 261's established `LoadCredential` pattern. A root-owned source file `/etc/jev-ocg-adapter/cpa-key` (directory 0700, file 0600) is copied by systemd into the protected service credential directory. The adapter reads only `$CREDENTIALS_DIRECTORY/cpa-key`, with no key in argv, ordinary environment, repo, logs, or unprivileged config. `LoadCredentialEncrypted` can replace the source later if that fits whiterock's credential administration. Root and the adapter service can access the runtime key. Fleet confirmed `agent` has passwordless sudo, so secret protection against agents relies on agreement and the Fleet guard, not a hard OS boundary. `agent` must not elevate to read it.

Add **a second dedicated client key** to the existing list at CPA `access.api-keys`, via a privileged no-echo operator procedure. Do not replace or expose the existing key. Stock CPA 8.0.26's [config access provider](https://github.com/router-for-me/CLIProxyAPI/blob/a27799ec2b0cc5a60e49970d5467829ceaab8131/internal/access/config_access/provider.go) treats keys as an unrestricted membership set; there is no per-key endpoint/model scope. Its [config loader](https://github.com/router-for-me/CLIProxyAPI/blob/a27799ec2b0cc5a60e49970d5467829ceaab8131/internal/config/config_load.go) and client-key type expose no credential-file/env substitution for `access.api-keys`. CPA therefore still needs the value **inline in its protected container config**, independently of the adapter's systemd credential. A dedicated key permits rotation/revocation, not least-privilege model enforcement.

**Installation gate (Fleet evidence):** `/var/lib/cliproxyapi/config.yaml` inside `cpa` is `cliproxyapi:cliproxyapi 0600`; `/var/lib/cliproxyapi/secrets` is `root:root 0700` and `secrets/client-key` is `root:root 0600`. `agent` (uid 1001, groups agent/users) cannot use Incus without sudo because its socket is root:incus-admin 0660. Thus the config is **unreadable without sudo**, and must remain **covered by the guard's secret-read rules**. Passwordless sudo defeats that as a hard access boundary. Agents must not read, print, diff, back up into their workspace, or edit real secrets themselves, including through sudo/Incus. Zach's privileged provisioning synchronizes the two dedicated-key copies without exposing them.

Fleet source commit `7f5bce6` adds `/etc/jev-ocg-adapter/cpa-key` and `/run/credentials/jev-ocg-adapter.service/*` to secret-read guards; Fleet says these ship with the **next guard release**. Verify that release is active before credential provisioning/installation, alongside existing CPA config/client-key guards. Rotation updates only the dedicated config-list entry and source credential, then restarts CPA and the adapter; it never modifies upstream account credentials. Residual risk: a stolen dedicated key can reach **every CPA upstream and model**. Mitigations are loopback-only placement, LoadCredential/private files, guard/operator discipline and separate rotation/revocation; model scoping would need a separate authorization layer.

The supplied service has no writable home, mounts, privileged devices or non-loopback network access. Its only durable output is prompt-free journald JSON. Any local process able to reach loopback may invoke this fixed-model classifier and incur usage; if whiterock's local-user threat model requires caller authentication, add a protected local auth layer before collection. No new agent-readable key is an acceptable substitute.

## Shadow, logs and quota coexistence

Merge `deploy/effort-router-shadow.yaml` into the existing config. It explicitly sets `shadow:true`, priority 0, the local classifier, 1500 ms timeout, and **Claude prefixes only**. Do not add gpt-6 initially. Shadow returns every request byte unchanged; manual effort overrides, old turns and unsupported formats/models are skipped. Decision/error records are prompt-free; details and remaining hash/state limitations are in [REVIEW.md](REVIEW.md).

Plugin state has no automatic cap: monitor private `shadow.jsonl` growth daily. Adapter JSON in journald includes time, chosen effort, fixed outcome, configured model, opaque session, elapsed ms, input/output/cached/reasoning token counts when provided. It has no prompt, Authorization, URL or upstream response text. Token counts allow cost estimates; they are not observed prices or final bills. Some timed-out upstream work may still be billed with no returned usage. Preserve CPA's known `request-log:false`, `debug:false`, `logging-to-file:true`/100 MB cap and usage statistics; do not enable body/debug logging for this experiment. Journald retention is separate from CPA's log cap and must be confirmed by Fleet.

Read quota-reset-router **v0.2.1**, tag commit `62b73e7c66b40bcef7b3eb2879f99d727290a740`, from its [public source](https://github.com/WebDevCaptain/quota-reset-router/tree/v0.2.1). It registers scheduler and management capabilities only; effort-router registers normalizer only. The scheduler's shadow mode returns an unhandled pick, retaining CPA's selection. No normalizer ordering conflict exists; CPA orders capability records by descending priority, and quota's priority 10 remains ahead of effort's 0 where applicable. Retain its exact config: enabled=true, priority=10, mode=shadow, poll_interval=5m, max_age=10m, request_timeout=10s. Keep CPA round-robin, session-affinity=true and request-retry=0. Adapter classification adds nested CPA traffic and usage statistics; quota polling/CPU and OCG routing share the same CPA process. Verify both plugin registrations and unchanged auth selection on synthetic traffic under the exception. **No live two-plugin coexistence test was performed.**

## Build and evidence (Mac only)

Tested toolchain: Go **1.26.9** darwin/arm64, Zig **0.17.0**, Apple clang for the native smoke build. Go archive was obtained from go.dev and SHA-256 verified: `f9bb7c0a02506c5d9bf0d1eb1f7ee6c7684f844ae49558308a0427b830e022cc` for `go1.26.9.darwin-arm64.tar.gz`. Existing Zig was used. Dependencies are pinned by go.mod/go.sum, including CPA v8.0.26.

From a checkout of this branch, with those tools on PATH:

```sh
go test ./...
go test -race ./...
go vet ./...
go build -buildmode=c-shared -o /tmp/effort-router-smoke.dylib .
go build -o /tmp/jev-ocg-adapter-smoke ./adapter
python3 scripts/abi-smoke.py /tmp/effort-router-smoke.dylib /tmp/jev-ocg-adapter-smoke
scripts/build-linux-amd64.sh 0.0.0-whiterock-shadow
(cd dist && shasum -a 256 -c SHA256SUMS)
file dist/effort-router.so dist/jev-ocg-adapter
```

The script sets CGO_ENABLED=1/GOOS=linux/GOARCH=amd64 and the executable `scripts/zig-linux-amd64-cc` wrapper (`zig cc -target x86_64-linux-gnu.2.36`). It produces a Linux/amd64 shared ELF plugin and a CGO_ENABLED=0 static Linux/amd64 adapter. `-trimpath -buildvcs=false` removes checkout/VCS variability. Artifact SHA-256 values are recorded in [LINUX-AMD64-SHA256SUMS](LINUX-AMD64-SHA256SUMS); `dist/SHA256SUMS` is generated locally. The plugin imports GLIBC symbols through 2.34 in the tested artifact; verify `ldd` on whiterock and do not treat cross-compilation as a Linux runtime test. The generated C header is only build output and is not installed.

Alternative later build in an authorized linux/amd64 `sbx` sandbox with Go 1.26.9, GCC/libc headers (not inside the credential-bearing CPA container):

```sh
mkdir -p dist
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=gcc \
  go build -buildvcs=false -trimpath -buildmode=c-shared \
  -ldflags '-s -w -X main.pluginVersion=0.0.0-whiterock-shadow' \
  -o dist/effort-router.so .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -buildvcs=false -trimpath -ldflags '-s -w' \
  -o dist/jev-ocg-adapter ./adapter
(cd dist && sha256sum effort-router.so jev-ocg-adapter > SHA256SUMS)
```

A sandbox rebuild has its own checksums; record and transfer that manifest with those binaries. Never compare a different compiler build to the Mac artifact hash as if they must match.

## Later install — Fleet under a hold exception

Exact nonsecret files: `dist/effort-router.so`, `dist/jev-ocg-adapter`, matching `dist/SHA256SUMS`, `deploy/jev-ocg-adapter.service`, `deploy/effort-router-shadow.yaml`, and both review/design documents. Local staged artifacts remain on the Mac, ignored by git. A repository push does not upload binaries or authorize an install.

1. Obtain the bounded exception (host user/service, reverse Incus proxy, plugin file, CPA config merge/restart, synthetic paid measurements and subsequent shadow week). Record current CPA binary/version, plugin registrations and nonsecret config names; arrange a **root-only** rollback backup. CPA config path is `/var/lib/cliproxyapi/config.yaml`; confirm the service UID/GID through Fleet (config owner is cliproxyapi:cliproxyapi). Don't dump the config. Leave existing quota plugin/file untouched. Stage the above files through the approved transfer mechanism; at the stage directory run `sha256sum -c SHA256SUMS`. Confirm x86-64 ELF and `ldd effort-router.so` resolves all dependencies. Review the source commit and checksum manifest together.
2. On host, create the static service identity and install nonsecret files:

   ```sh
   sudo useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin jev-ocg-adapter
   sudo install -d -o root -g root -m 0755 /usr/local/libexec
   sudo install -m 0755 jev-ocg-adapter /usr/local/libexec/jev-ocg-adapter
   sudo install -m 0644 jev-ocg-adapter.service /etc/systemd/system/jev-ocg-adapter.service
   sudo install -d -o root -g root -m 0700 /etc/jev-ocg-adapter
   ```

   If the user already exists, verify its identity rather than rerunning useradd. Zach provisions the 0600 credential and dedicated CPA key using the protected procedure above. No command here accepts a real key in argv or prints it.
3. Install the plugin through staging (example staging filename, approved transfer still required):

   ```sh
   incus file push effort-router.so cpa/tmp/effort-router-shadow.so
   incus exec cpa -- install -o root -g root -m 0755 /tmp/effort-router-shadow.so /opt/cliproxyapi/plugins/effort-router.so
   incus exec cpa -- rm /tmp/effort-router-shadow.so
   ```

   Create `/var/lib/cliproxyapi/effort-router` **inside cpa**, owner=the observed CPA UID/GID, mode=0700; pre-create empty `shadow.jsonl`, same owner, mode=0600, only if absent. Use a new path if an old log exists. Fleet can execute `incus exec cpa -- install -d -m 0700 -o <observed-CPA-UID> -g <observed-CPA-GID> /var/lib/cliproxyapi/effort-router` after substituting those nonsecret values. Do not overwrite a state file.
4. Add the reverse loopback proxy (confirm the device name is unused):

   ```sh
   incus config device add cpa effort-classifier proxy \
     bind=instance listen=tcp:127.0.0.1:8765 connect=tcp:127.0.0.1:8765
   sudo systemctl daemon-reload
   sudo systemctl enable --now jev-ocg-adapter
   curl --fail --silent --output /dev/null http://127.0.0.1:8765/healthz
   incus exec cpa -- curl --fail --silent --output /dev/null http://127.0.0.1:8765/healthz
   ```

   Healthz=204 checks reachability only; it makes no CPA/model call and does not prove key validity. Confirm only loopback listeners and enforce the unit's loopback filter. A reverse-proxy/LoadCredential failure is an installation failure, not permission to use a LAN bind or readable secret.
5. Merge the supplied effort subtree and add the protected client key. The **only** routing config addition is:

   ```yaml
   plugins:
     configs:
       effort-router:
         enabled: true
         priority: 0
         shadow: true
         jev-url: http://127.0.0.1:8765/v1/systemone
         jev-model: jev-latest
         timeout-ms: 1500
         models: [claude-fable-5-1, claude-opus-5-5]
         baseline-effort: medium
         state-file: /var/lib/cliproxyapi/effort-router/shadow.jsonl
   # access.api-keys: append the protected dedicated key, preserving the existing entry.
   ```

   Do not replace the whole plugins/access maps, alter quota settings, or enable debug/request logs. Restart using `incus exec cpa -- systemctl restart cliproxyapi` (fleet-reported service name). Inspect plugin registration/version via a nonsecret filtered interface. Confirm both plugin libraries load, no ABI/schema errors occur, the dedicated key authenticates, and a synthetic adapter request succeeds with the exact schema and session header. No key-bearing curl command/log may be placed in the agent transcript.
6. Before real shadow turns, validate Claude Code 2.1.296 directives and default effort against this plugin's baseline assumption. Establish synthetic baseline/request equivalence with and without the plugin in a **dummy/mock** CPA setup or instrumentation that reports only equality and sizes; don't capture user bodies. Repeat the tests for manual effort, tool loops, restarts and concurrent classifications. Observe latency/error/token/selection summaries only. If a credential-read or unexpected logging issue appears, stop collection and roll back.

## Qualification and enablement

Collect approximately one week of **Claude-only shadow** decisions after those gates. Compare picks to a small operator-labelled nonsecret corpus and task outcome review (without retaining prompts), incremental classifier token cost versus provider billing, timing/timeout rates, skipped overrides and suspected over/under-effort. Log counts and distributions, not task text. Shadow does **not** measure performance of changed effort or validate cache preservation; it only measures picks and overhead. An approved small active Claude canary with fresh sessions is required after shadow, with an operator-approved rubric and rollback thresholds. Keep quota-reset-router in shadow throughout.

To enable that canary, change only `shadow:false` and `state-file:/var/lib/cliproxyapi/effort-router/active.jsonl`, create its private file/directory permissions, restart CPA and start fresh client conversations. Do not replay shadow picks into ongoing conversations. Preserve the active state as long as sessions depend on it.

Only after Claude qualification, compare:

- **A: native Codex/Responses.** Still skipped by this branch. Before implementing support, reproduce `/responses/compact` rejection on a dummy CPA/mock path and establish a cache-safe strategy through compacted histories, tool loops and restarts. Confirm actual GPT-6 acceptance of configuration_update. Do not fix it by stripping historical updates only during compact without proving replay/cache behavior.
- **B: OpenAI models through Claude Code.** This branch supports the existing translated codex configuration_update path when FromFormat=claude. Test Claude Code 2.1.296 translation, manual effort mapping, long sessions, tools, cost/quality and cache counters against A before choosing. GPT-6 routing is not enabled in the initial install config.

Recommendation: compare the smaller B path after Claude, and invest in A only if native Codex workflow benefits justify the compaction work. Zach chooses the client path and thresholds; no automatic enablement occurs.

## Rollback

For shadow: set `plugins.configs.effort-router.enabled:false`, restart CPA and verify quota-reset-router remains registered/in shadow. Stop/disable `jev-ocg-adapter`; remove the `effort-classifier` Incus device; revoke only the dedicated CPA key via privileged config update/restart. Preserve or privately archive prompt-free experiment logs and restore the protected config backup if needed. The disabled plugin file can be moved outside CPA's plugin directory while CPA is stopped. Never replace/remove a library while live calls use it.

For active: disable routing/restart and start fresh client sessions, expecting a cache-prefix change for histories that had inserts. Keep the active decision file for forensic comparison or a deliberate re-enable; deleting it is not rollback. Restore only the backed-up effort subtree/key addition or prior library version, preserving quota configuration and all upstream credentials.

## Operator decisions still needed

- Hold exception and live measurement budget: recommend a bounded install plus synthetic latency/schema checks, followed by the shadow week; no active routing in that exception.
- Classifier comparison: recommend OCG DeepSeek as implemented, plus free Zen Jev if a protected Zen credential is approved; choose by measured latency/accuracy/cost, not the temporary free label alone.
- After evidence: approve an active Claude canary and choose A/B later; recommend B first for the lower implementation burden, with A held until compaction is proven.

Config permissions and guard-source additions are confirmed by Fleet above. Active guard release, service identity, reverse-proxy support, structured-output support, latency, journald retention and two-plugin runtime behavior remain Fleet verification gates, not facts that only Zach can answer.
