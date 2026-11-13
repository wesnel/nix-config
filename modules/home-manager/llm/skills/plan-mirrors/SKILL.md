---
name: plan-mirrors
description: Publish a session’s hosted plan view when its plan is created, meaningful progress is recorded, a decision is settled, or work is handed back; reconcile an uncertain publication before updating again.
---

# Plan mirrors

Run `plan-dash snapshot`, `plan-dash claim` and `plan-dash release`
as bare commands in separate shell calls, with the session’s worktree as
the tool’s working directory. Pass paths as arguments; use no wrapper,
environment override, pipe, redirection or chained command. Claude’s
narrow exclusions run these commands outside its sandbox. In Codex set
`sandbox_permissions: "require_escalated"`; the installed command rules
approve these three prefixes while mirror state stays outside writable
roots. An absent rule or an explicit refusal remains a boundary to report.

## Session view

1. Run `plan-dash snapshot [PLAN]` in the session’s worktree. Use PLAN
   when selection is ambiguous. Keep its `file`, `hash`, `publishedHash`,
   `title`, `plan`, `session`, `backend`, `page`, `block`,
   `blockHash`, `createKey` and `pending`.
2. If publication is pending or its result is unknown, report that it
   needs operator reconciliation. Preserve its destination and pending
   operation; do not retry or create a replacement. Otherwise, skip
   publication when a bound page’s `publishedHash` equals `hash`.
3. Publish through the backend’s native tool, using the exact arguments
   below. The generated contents are published unchanged.
4. After a native call, run `plan-dash snapshot [PLAN]` again. The
   post-tool hook records the native receipt; only the returned binding
   supplies the destination for the next call. Page creation must return
   the page binding; visualization or Artifact publication must also
   confirm `publishedHash == hash`. If that confirmation is absent,
   report the mirror as pending.
5. Verify the hosted view and link it in the hand-back. URLs under
   References are navigation, never publication authority.

### Pages

For an unbound session, call
`mcp__codex_apps__chatgpt_space__create_page` with exactly
`{title: title, idempotency_key: createKey}` from the snapshot.
This creates the private session page: omit parent, space, initial blocks
and all other arguments. Read the next snapshot for the bound `page`
before adding its visualization.

Call `mcp__codex_apps__chatgpt_space__create_page_visualization` with
`{page_id: page, title: title, html: contents}`, where `contents` is the
exact bytes read from `file`. For an existing `block`, also supply
`replace_block: {block_id: block, expected_hash: blockHash}`.
Omit `after_block_id`; `base_sequence` is optional. Use only the
snapshot’s page, block and hash, including when replacing a view.

### Artifact

Call Artifact with
`{command: "publish", file_path: file, capabilities: {}}`.
When the snapshot has a bound or configured `page`, also supply
`url: page`; omit it for initial session creation. The native post-tool
hook must confirm the published URL before the binding is usable.
An unfamiliar receipt stays pending even if the tool appeared to succeed.

The generated file is publication input. A local file or a separate static
output does not complete a hosted mirror update. When native tools are
unavailable, report the mirror as pending and keep working on the plan.

## Aggregate views

The Space aggregate has one writer: `plan-dash refresh`, run by the Mac
watcher against the configured `spacePage`. Session views use their own
bindings. Leave the aggregate Space block to refresh.

For the configured Claude aggregate:

1. Run `plan-dash claim`. Continue only for `status: claimed`; `busy`
   leaves publication to the recorded owner.
2. Run `plan-dash snapshot --open`, then publish with the Artifact
   arguments above, including the claim’s configured `page` as `url`.
3. Only after confirmed success, run `plan-dash release`. A failed or
   unknown result leaves the claim in place for reconciliation. Claims
   never expire automatically; check the hosted artifact and resolve the
   uncertain operation before releasing or retrying.

An unconfigured aggregate target needs operator bootstrap: the operator
creates the destination, sets its artifact URL or Space page ID in
`wgn.home.llm.agentPlans.claudePage` or `spacePage`, then activates.
Verify the destination’s audience before publication: embedded HTML follows
the target’s sharing settings.
Report aggregate publication as pending until that is complete. Session
page creation grants only a session binding. The host’s watcher refreshes
the configured hosted page; it does not create or host that page.
