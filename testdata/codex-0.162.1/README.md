# Synthetic native request shapes

These are hand-authored **shapes**, not traffic captures. Text, IDs, code inputs and the compaction blob are fabricated; no real prompts, credentials or encrypted content appear here.

Source: OpenAI Codex tag `rust-v0.162.1`, commit `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`:

- [codex-api/src/common.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/codex-api/src/common.rs): ResponsesApiRequest fields and response.create fields.
- [core/src/client.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/core/src/client.rs): developer prefix, stream=true, store=false, reasoning, tool_choice, cache key, include and metadata.
- [tools/src/tool_spec_tests.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tools/src/tool_spec_tests.rs): namespace/custom exec tool wire structure; the grammar/description in our fixtures are synthetic.
- [protocol/src/models.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/protocol/src/models.rs): message, custom call/output, configuration_update, compaction_trigger, opaque compaction item.
- [core/src/compact_remote_v2_attempt.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/core/src/compact_remote_v2_attempt.rs): streaming compaction request appends compaction_trigger.
- [core/src/session/reasoning_effort.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/core/src/session/reasoning_effort.rs): fixed request baseline and harness-owned update lifecycle across compaction.

`standalone-compact.json` represents CPA 8.0.26's separately supported `/v1/responses/compact` HTTP shape, not a claim that every Codex 0.162.1 `/compact` operation uses that endpoint. The pinned client also uses streamed compaction triggers; the proof must test both.

`native_test.go` verifies replay, byte-preserving shadow, explicit update ownership, context-window reset, session separation and unsupported controls. `scripts/cpa-mock-proof.py` exercises the official checksum-verified CPA 8.0.26 binary with the real native plugin/gateway and localhost fakes; mock cache numbers never count as live caching evidence.
