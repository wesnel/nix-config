# Codex allowance admission

Review, ask, diagram critique and direct `CodexRun` calls share one admission
state per invocation. Local holds, unreadable state, the failed-write latch
and applicable review-loop or model-family restrictions are checked before
starting a provider. Diagram work checks both providers before probing and
again before proceeding. Availability status, clear and prompt export remain
local operations.

An eligible Codex invocation makes one model-free allowance query before
login status, sandbox checks or model execution. The reader sends only
`initialize`, `initialized` and `account/rateLimits/read`. Initialization sets
`explicitGatewayOauth: true`; the read sets `excludeResetCreditDetails: true`
and `supportsLunaReserve: false`. It neither requests nor consumes reset
credits. Incoming server requests abort the read; no login, approval, tool,
thread or turn RPC is sent.

Only an unambiguous `ordinaryUsageAllowed: false` records exhaustion through
the existing host-local provider store. The invocation stays blocked even if
that write fails. True, null, absent fields and transport, protocol,
authentication or unsupported-method errors leave existing eligibility
unchanged. Percentages and reset times do not establish recovery. Local
holds are checked again after the query, so a concurrent disable wins.
Terminal model failures still use the existing quota classifier.

The indefinite hold applies to the current account on this host. After
recovery or an account change, `git delegate availability --clear codex`
permits another attempt; its preflight can block again. A denied review
returns the existing native-subagent fallback instructions. Already admitted
concurrent calls may remain in flight.

`allowance.go` resolves the same executable as `CodexRun`, starts it in a
fresh temporary directory and points SQLite there. Strict configuration,
the OpenAI provider, disabled plugins/apps/hooks/rollout maintenance and
disabled analytics/telemetry reduce startup work. User and managed
configuration, ordinary authentication, cache and catalog loading can still
occur. This is not general runtime isolation or a guarantee of zero writes.
No live app-server or account response is exercised by these tests.

The reader has a five-second deadline, a 64 KiB frame limit, a 256 KiB
combined stdout/stderr budget and a 32-notification limit. It terminates its
process group on success and failure, reaps the direct child and cleans up
the temporary directory. Cleanup uses the existing bounded `stopGroup`
operation. Raw replies and diagnostics are discarded rather than retained
in logs or availability state.

Run the fake-executable admission and protocol fixtures with
`go test ./internal/review -run '^TestAllowance' -count=1` from
`overlays/agent-tools`. They assert exact requests and startup arguments,
unknown outcomes, local policy ordering, shared queries, persistence
failures, concurrent disables and bounded cleanup without account access.
