# Whiterock throwaway-CPA proof notes

Zach confirmed through Fleet that the live proof runs **on whiterock**, using the real Linux plugin and whiterock's T3. **The Mac execution plan is not approved.** These notes replace the location, setup, identity, credential and cleanup steps in [CODEX-PROOF-PLAN.md](CODEX-PROOF-PLAN.md); its native Codex acceptance criteria, synthetic tasks, evidence rules and run sequence still apply. The new whiterock Fleet session is the writer and host operator. This Mac session has run no credentialed proof and performs no whiterock operations.

Approved limits remain **40 GPT-6.1-Sol requests, 4 Claude requests, 40 Jev classifications, 45 minutes and 20,000 observed output tokens**, including tool/compaction requests and reasoning where counted in output. Count totals across restarts, modes and clients: the relay's caps reset with its process. Jev is primary; DeepSeek comparison is outside this proof. Passing this proof does not authorize production deployment.

## Isolation and topology

Use a new throwaway Incus container, proposed name **`cpa-effort-proof`**, with CPA **8.0.26** and an empty auth directory. Never use the production container named **`cpa`**, its config, credentials, auth directory, client keys, listeners or T3 provider. Do not copy the production container or take a credential-bearing snapshot as a starting point. Fleet prepares a clean image and fetches/verifies the official Linux/amd64 CPA release checksum independently.

Proposed loopback layout (Fleet checks availability before creating anything):

| Component | Location | Address |
| --- | --- | --- |
| Throwaway CPA | `cpa-effort-proof` | `127.0.0.1:8318` |
| CPA host exposure | whiterock host, non-NAT Incus proxy | `127.0.0.1:8318` -> proof container `127.0.0.1:8318` |
| Numeric-usage relay | whiterock host | `127.0.0.1:8319` -> host `127.0.0.1:8318` |
| Jev proof adapter | whiterock host, separate service | `127.0.0.1:8766` |
| Classifier reverse exposure | proof container, non-NAT Incus proxy | proof container `127.0.0.1:8766` -> host `127.0.0.1:8766` |

This preserves the relay's current hard-coded ports. Container and host loopback are different namespaces. Proposed proxy commands, **for the new Fleet operator only**, after verifying device names/ports and installed Incus support:

```sh
incus config device add cpa-effort-proof proof-api proxy \
  bind=host listen=tcp:127.0.0.1:8318 connect=tcp:127.0.0.1:8318
incus config device add cpa-effort-proof proof-classifier proxy \
  bind=instance listen=tcp:127.0.0.1:8766 connect=tcp:127.0.0.1:8766
```

Neither proxy belongs on `cpa`. No public/LAN listener is needed. Production host port 9444, container port 8317 and adapter port 8765 are never proof destinations. If proposed ports are occupied, stop and revise the proof-specific config/relay explicitly; don't connect to the occupant or restart it.

Use the existing Linux artifacts, not a Darwin library:

- `dist/effort-router.so`, SHA-256 `1a12acfb7f41b3da43547dc28dbe6b0bf3db7300336f22a251d631d8b98db789`.
- `dist/jev-ocg-adapter`, SHA-256 `c3af05dfac8e85a5dc59f3fd144c86309d89e228af424f55ef9002f6dfe6f22c`.

The build/alternate Linux sandbox recipe and manifest are in [WHITEROCK-DESIGN.md](WHITEROCK-DESIGN.md) and [LINUX-AMD64-SHA256SUMS](LINUX-AMD64-SHA256SUMS). Transfer only nonsecret artifacts via Fleet's approved mechanism; verify hashes and `ldd` inside the proof container, then establish actual Linux library loading. A different rebuild gets its own manifest. None of the Linux runtime checks have been performed by this Mac session.

Adapt the public [CPA template](../deploy/mac-proof/cpa-config.json): use the proof container's private prefix below, install `effort-router.so` into its private `plugins/` directory, keep port 8318 and classifier URL `http://127.0.0.1:8766/v1/systemone`. Keep shadow initially, native baseline high, Claude baseline medium, the exact model prefixes and separate shadow/active state files. No production config merge is part of this proof. Do not enable other credential-bearing providers or scheduler plugins by copying production settings. Keep request logging/debug off, retries zero, management panel disabled, and every raw/error log private. Test access keys may remain empty on these short-lived loopback listeners as proposed in the original plan; any local process can then invoke them. Do not point the proof adapter at production CPA to avoid that exposure.

## Private ownership and OAuth over SSH

Use **`/var/lib/effort-router-proof/private/`** as the protected prefix **both on the host and inside the throwaway container**. Identical path spelling does not make the files shared: do not mount the auth directory onto the host or into production.

- Inside `cpa-effort-proof`: root-owned 0700 prefix and `cpa-auth/`, private config/state/proof/raw logs 0600 under umask 077. Run the throwaway CPA with the corresponding permitted identity (root in this proposed isolated container); if Fleet chooses another CPA UID, that UID must own its private 0700 tree. Set `oauth.auth-dir` to `/var/lib/effort-router-proof/private/cpa-auth`. Run CPA and login with their working directory inside private so forced error dumps remain there.
- On host: Zach creates an **agent-owned 0700** private tree for T3's isolated Codex home and relay/client logs, files 0600. Root ownership is also valid where only root-operated processes need access, but a root-owned 0700 parent cannot be traversed by an agent-owned T3 process. Do not relax permissions to solve that; use separate appropriately owned private trees and extend the guards if changing their paths.
- Agents must not read/print/edit/search these private trees, including through sudo, Incus file pull/exec, alternate mounts, snapshots/backups or scripts. The running approved services may use credential paths; agents receive only fixed outcomes and exported allowlisted evidence. Whiterock's `agent` has passwordless sudo, so this is guard/operator discipline, **not a hard boundary** even for root-owned data. Agent-owned data is not an OS read boundary at all.

**Zach performs fresh OAuth login himself over SSH**, outside an agent terminal/transcript, selecting the approved subscription accounts. Fleet supplies the verified proof binary location; example path below is a deployment choice, not an observed installed file:

```sh
# Zach's SSH terminal; all paths refer to cpa-effort-proof, never cpa.
incus exec cpa-effort-proof --cwd /var/lib/effort-router-proof/private -- \
  /usr/local/bin/cli-proxy-api \
  -config /var/lib/effort-router-proof/private/cpa-config.yaml -codex-login -no-browser
incus exec cpa-effort-proof --cwd /var/lib/effort-router-proof/private -- \
  /usr/local/bin/cli-proxy-api \
  -config /var/lib/effort-router-proof/private/cpa-config.yaml -claude-login -no-browser
```

Fleet must ensure the login command inherits umask 077 or runs through an operator-owned wrapper that sets it. Do not paste authorization URLs, callback query strings/codes, tokens or account identifiers into chat. CPA writes only to the new auth directory; no production/existing auth is imported. The Claude login is for the approved four-call compatibility leg.

CPA's pinned source defaults to Codex callback **1455** and Claude callback **54545**. For a browser on Zach's workstation, Fleet can prepare temporary **proof-container-only**, host-loopback callback proxies, then Zach opens SSH local forwards himself:

```sh
ssh -N -o ExitOnForwardFailure=yes \
  -L 1455:127.0.0.1:1455 -L 54545:127.0.0.1:54545 whiterock
```

Both ends must be free; confirm the actual CPA login flow's callback/redirect before use. Never displace a production/local callback listener. If the installed flow offers manual completion instead, Zach handles that privately. Remove temporary callback proxies/tunnels after login. Guards must cover the auth/config paths **before** tokens are written. No agent collects OAuth browser state or login output.

## Zen credential and separate proof service

Use a distinct host service identity/unit **`jev-ocg-adapter-proof` / `jev-ocg-adapter-proof.service`**, listener 127.0.0.1:8766. Zach provisions a proof-only Zen key into **`/etc/jev-ocg-adapter-proof/zen-key`**, root-owned 0600 under a root-owned 0700 directory. The service uses:

```ini
LoadCredential=zen-key:/etc/jev-ocg-adapter-proof/zen-key
ExecStart=/usr/local/libexec/jev-ocg-adapter-proof -listen 127.0.0.1:8766 -backend jev -jev-model jev-1.13-free -timeout 1200ms
UMask=0077
StandardOutput=append:/var/lib/effort-router-proof/private/logs/adapter.log
StandardError=append:/var/lib/effort-router-proof/private/logs/adapter.err
```

Adapt the existing service's hardening, keeping outbound HTTPS/DNS for Zen. Systemd's manager can open the pre-created private log files for the service without making the host private directory traversable by the adapter UID. Avoid a service WorkingDirectory inside an inaccessible tree. Read only the runtime `zen-key` from `$CREDENTIALS_DIRECTORY`; no key in argv, plain environment or repo. Add source/runtime paths to the guard before provisioning and verify the enforcing release. Existing production Zen/service paths remain untouched. Jev-only needs **no CPA adapter credential or client-key addition**. No OCG or production CPA call is needed.

## Whiterock T3 and native CLI

Create a **test provider in whiterock's T3**, not a change to its normal provider. It must drive **Codex CLI 0.162.1**, GPT-6.1-Sol, code mode, single agent, the proof Responses endpoint `http://127.0.0.1:8319/v1`. Verify the installed binary version without inspecting auth. Start fresh synthetic conversations/workspace. No real project/prompt content belongs in this proof.

Use isolated **`CODEX_HOME=/var/lib/effort-router-proof/private/codex-home`**, agent-owned 0700, config/session files 0600. Adapt [codex-config.toml](../deploy/mac-proof/codex-config.toml) without importing existing settings/auth. Set CODEX_HOME only on the child CLI/app-server process, never by replacing HOME globally. A proof launch wrapper may redirect stderr to `private/logs/`; stdout must remain the T3 app-server protocol. Disable raw protocol logging or redirect **all raw process/error logs** into the guarded private tree before starting. T3's synthetic conversation storage is expected; raw protocol/error logs must not be exported. If T3 cannot isolate its child environment or private raw-log placement, stop that leg and leave the T3 gate unresolved. Record only nonsecret binary/model/mode/transport receipts.

The current relay and template qualify **HTTP/SSE**, with WebSockets disabled. Fleet must check the production transport by nonsecret means. If the production provider uses WebSockets, an HTTP-only proof is insufficient for that production transport: extend the approved test rather than claiming coverage. Both standalone compact and the client's streaming compaction must still pass. Native CLI runs on whiterock with the same isolated home/provider; its `/compact` and tool calls are independently evidenced from T3.

## Guard additions before setup

The new Fleet writer/operator adds and activates these **secret-read paths**, including recursive directory contents, and verifies interception before any private setup/login:

| Path | Protected content |
| --- | --- |
| `/var/lib/effort-router-proof/private` and `/var/lib/effort-router-proof/private/**` | Host client homes, container auth/config/state, raw/error logs and private proof files |
| `/etc/jev-ocg-adapter-proof/zen-key` | Proof-only Zen source credential |
| `/run/credentials/jev-ocg-adapter-proof.service/*` | Systemd proof service runtime credential |

Verify guard matching also covers container-oriented reads/exports (`incus exec`, `incus file pull` and equivalent backup/snapshot or mount routes), not just direct host `cat`/read. Do not validate using real secret contents or attempt a bypass. New credential/log/home paths or different service names require corresponding guard additions **before use**. Preserve production coverage from Fleet source `7f5bce6` and `2265bf2`; their production paths/wildcards do **not** automatically cover this differently named proof service. This Mac session does not change Fleet source or claim a guard release is installed.

Only Zach/operator runs [proof-export.py](../scripts/proof-export.py) against the host private evidence tree and exclusively creates an allowlisted export outside it. Because normalizer records originate **inside the proof container**, operator-only transfer must stage just `normalizer-proof.jsonl` into host private, never auth or raw logs. The relay's `usage-proof.jsonl` is already host private. Agents read only the final HMAC-redacted export, containing fixed shapes/equality/statuses and numeric usage. Positive actual cache counters remain mandatory; mock cache values and normalizer-stage equality alone do not satisfy the gate.

## Fleet completion and cleanup

Fleet executes the original shadow/active/CLI/T3/compaction/Claude sequence within the caps, records which synthetic run belongs to each client without private session IDs, and reports any unresolved cache, latency, transport or Linux runtime evidence. State files and sessions are fresh when switching to active; no production enablement follows automatically. Keep rollback immediate: stop the proof processes only.

Zach/operator stops the proof provider/clients, relay, CPA and proof adapter; revokes the proof-only Zen key; logs out/revokes throwaway Codex/Claude OAuth authorizations where supported; removes proof callback/API/classifier proxies, proof unit/identity and private host files; then deletes **`cpa-effort-proof` only** after any approved allowlisted export. Remove any proof snapshots/backups containing tokens. Provider-side revocation remains separate from deleting local auth. No cleanup command targets production `cpa` or its directories. Retain the redacted evidence, reviewed source commit and Linux artifact manifests in the Fleet writer's record.
