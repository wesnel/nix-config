# Space dashboard snapshots

`plan-dash export` collects the existing allowlisted plan, GitHub, review
and digest documents. `plan-dash space prepare` reads those saved documents,
including retained last-good documents, and writes
`$XDG_STATE_HOME/agent-plans/space/snapshot.html`. The command reports
`status: prepared`; preparation is separate from publication.

The snapshot uses the same renderer as the Claude Artifact. It labels the
collection time from `meta/sync.json`, retains GitHub's collection time and
stale flag, and displays diagram source. Viewing, opening diagrams and
expanding details require no model or network calls. Relative ages advance
while the snapshot stays open. Publication verification time is stored in
the receipt separately, so an HTML file never claims a future publication.

The full HTML must fit the Pages limit of 256 KiB of UTF-8. Malformed,
invalid UTF-8 or oversized input fails preparation before replacing the
previous snapshot. The Space adapter adds no truncation to the exporter's
existing field and list limits. Keep sensitive data out of plans: the
exporter's redaction recognizes known shapes, not all sensitive content.

## Publication target

The operator configures an existing writable Page and its owned visualization
block in `$XDG_CONFIG_HOME/agent-tools/agent-plans-space.json`:

```json
{
  "page_id": "page_REPLACE_WITH_OBSERVED_ID",
  "block_id": "REPLACE_WITH_OBSERVED_BLOCK_ID",
  "thread_id": "REPLACE_WITH_EXISTING_CODEX_THREAD_ID"
}
```

Copy exact IDs from authorized reads. The block must be a standalone
`![Agent plans](visualize:FILE_ID)` embed owned by this publisher. The
configured thread must already exist on the publishing host, with the Pages
plugin connected and authorized. Use the installed Nix package: its config
and state paths are baked into the binary. Development builds use the test
environment's XDG paths and are not an authorization boundary.

The guard protects this configuration and the publication journal from agent
edits, including writes through links. There are no target, endpoint, tool,
approval or config-path flags. The native guard reviews only the exact
`read_page` and `create_page_visualization` Pages tools on `codex_apps`, in
their raw `chatgpt_space__...` and executor-normalized `chatgpt_space_...`
spellings. Unrelated tools remain refused. A native visualization write
outside the configured Page/block requires confirmation; Codex refuses it
because its guard cannot ask for confirmation.

`plan-dash space publish` resumes the configured thread through the supported
[Codex app-server protocol](https://learn.chatgpt.com/docs/app-server),
discovers its connected Pages tools, reads the owned block and sends its
observed hash and optional sequence with the complete snapshot. It records
a successful publication only after a successful receipt and matching
readback. It starts no model turn and does not inspect account credentials.
Any approval, authentication or other server request stops the bridge.

The publishing host must support the observed app-server and Pages schemas,
including `mcpServer/tool/call`, structured read results and visualization
receipts. A replacement receipt must retain the configured block ID. Missing
tools, changed identities or unsupported receipts fail closed. Fake bridge
tests cover these contracts; live plugin authorization, replacement identity
and viewer behavior must be verified on the intended host by the operator
before unattended use. Building this repository does not activate the
guard, publish a Page or install a schedule.

## Recovery

The publisher locks one local writer and writes a pending attempt to
`$XDG_STATE_HOME/agent-plans/space/publication.json` before upload. This file
also retains the last verified receipt: Page ID, block ID, snapshot hash,
block hash, visualization Markdown and verification time.

A later invocation reads the Page before deciding what to do. If a pending
attempt has an observed uploaded file and readback confirms that exact
embed, it records the recovered publication without uploading again. If
the saved outcome or file is unknown, the target changed, or the block no
longer matches, publication stops for operator reconciliation. The guard
also refuses a native upload while this pending record exists.

Do not clear a pending record merely to retry. The operator must inspect the
same Page and owned block and, where an upload was returned, its file access.
Reuse an uploaded visualization through the authorized Pages workflow when
appropriate. A changed block ID requires an explicit target configuration
change. Keep the previous verified receipt while resolving the pending
attempt. The bridge never grants a permission request or retries an upload
whose outcome it cannot establish.

The snapshot and transport are deterministic data operations, so they do not
probe model quota. Any workflow that adds inference must apply the shared
provider availability gate before starting that inference.

## Checks

Run the focused tests from `overlays/agent-tools`:

```sh
go test ./cmd/plan-dash ./internal/agentplans ./internal/export ./internal/guard
```

`TestSnapshotViewer` executes the renderer in Node with network and external
capabilities forbidden. It reports a skip when Node is absent; the Nix
package supplies Node to its check phase. `TestSpacePublication` validates
the JSON-RPC requests and uses response shapes from the installed
app-server schemas and Pages tool declarations, without calling a live
provider or publishing a Page.
