# Codex configuration

Activation reconciles `~/.codex/config.toml` once. The `wgn` permission profile
extends `:workspace`, uses the reviewed loopback and Nix daemon proxy, and
denies the shared LLM credential paths and workspace environment files.
The Nix daemon performs builds outside the command sandbox. The profile is
a user default: command-line options and higher-precedence configuration
can override it. It is not an administrator-enforced requirement.

The reconciler removes the legacy sandbox keys that override named profiles.
It owns the complete managed profile and proxy policy, while preserving
nonconflicting workspace roots, environment entries, unrelated settings and
comments outside replaced tables. Plans and tool caches retain their existing
root ownership files. A temporary pending intent records roots across an
interrupted write; the next reconciliation removes an abandoned generation
even when the new desired paths differ. Successful reconciliation clears
that intent.

Conflicting roots or explicit environment values fail before configuration
or ownership changes. A lock file may be created. Legacy environment filters
migrate to the canonical map. Legacy temporary-directory restrictions require
an explicit migration instead of being silently removed. Diagnostics name
conflicting variables without printing their values. Environment exclusions
filter inherited variables; programs can still create their own environment.

The shared denial list is unchanged for Claude. Codex additionally applies
the shared environment-file patterns beneath each runtime workspace and
profile root. Linux deny globs are enumerated at process startup: files
created later can remain readable to that process. Unbounded patterns may
need `glob_scan_max_depth` on Linux; this configuration does not choose an
unverified depth. Linux runtime coverage remains unverified. See the
[permissions documentation](https://learn.chatgpt.com/docs/permissions).

Trusted SessionStart hooks restore bounded plan context for startup, resume,
clear and fork. Compaction retains the existing reminder. Codex clear never
consumes Claude's handover or assigns plan ownership. Native subagent hooks
can carry the parent's session ID; ordinary thread-aware plan lookup does
not prove the identity of a hook process or disambiguate automatic post-tool
records between plans sharing a worktree.

Hooks are not a complete failure-closed boundary. Untrusted hooks are skipped;
some failures, timeouts and malformed responses allow execution to continue.
Hosted tools and some specialized paths do not pass through PreToolUse, and
writing to a running shell does not rerun it. Installed Codex 0.165.0's event
schema includes neither Notification nor StopFailure. The TUI supports its
own terminal notifications, separately from hooks. See the official
[hooks](https://learn.chatgpt.com/docs/hooks) and
[configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference).

Native reviewer roles inherit the parent's sandbox, approvals and MCP
configuration. Their read-only instructions are advisory. Supported feature
reductions can remove shell, apps, plugins, memories and permission-request
tools; removing shell also removes the usual repository inspection tools
and does not establish a role-specific write ban.

| Capability | Supported surface | Evidence here |
| --- | --- | --- |
| Plan restoration | SessionStart startup/resume/clear/fork; compact reminder | Supplied payload and generated-hook tests; live dispatch unverified |
| Tool guard | Trusted PreToolUse and PostToolUse hooks on supported paths | Generated configuration and existing guard tests; failure-closed runtime unverified |
| Notifications | Native TUI terminal notifications | Official configuration reference; no Notification or StopFailure event in installed schema |
| Reviewer permissions | Parent sandbox and approvals | Official custom-agent documentation; role-specific write prohibition unavailable |
| Read denial | Named filesystem profile | Harmless exact/glob macOS command probes; credential paths and Linux runtime unverified |

`codex-settings` checks the generated activation and hooks plus isolated
reconciler fixtures, including crash recovery and rejected grants. Lifecycle
tests exercise supplied hook payloads without model calls. Harmless macOS
schema-file probes demonstrated exact and glob read denial, but did not prove
credential-file coverage or live hook dispatch. `codex sandbox` executes a
command without a model turn; it does not exercise the model's tool router.
