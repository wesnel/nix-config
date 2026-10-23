# nix-config

`flake.nix` wires everything. Machines in `machines/{darwin,nixos,home-manager}/<name>`
enable modules from `modules/{darwin,nixos,home-manager}/<name>`, whose options
sit under `wgn.darwin`, `wgn.nixos` and `wgn.home`. Packages are in `overlays/`;
the agent tooling is the Go module `overlays/agent-tools`. `ngrok` is the
aarch64-darwin work Mac; `devbox` is an x86_64-linux Home Manager machine.

## Checking a change

Run these from the repository root unless a row names a directory. Nix sees
only files git tracks: `git add` a new file before building. Building is as
far as a change goes here; switching a machine is the operator's, with the
commands in the README's "updating systems".

| Changed | Run |
| --- | --- |
| `overlays/agent-tools` | in that directory: `gofmt -l .` (prints nothing), `go build ./...`, `go vet ./...`, `go test -count=1 ./...`; then `nix build --no-link .#checks.aarch64-darwin.agent-tools-lint` (gosec, which CI also runs) |
| skills or `AGENTS.md` under `modules/home-manager/llm`, agent files under `modules/home-manager/claude/agents`, `README.md`, or `pr-loop`'s subcommands and agent-tools' messages | `nix build --no-link .#checks.aarch64-darwin.agent-names`: every sub-agent type and `pr-loop` subcommand they name exists |
| `modules/home-manager/claude`, `modules/home-manager/llm`, or `overlays/agent-tools/internal/guard/mcp.json` | `nix build --no-link .#checks.aarch64-darwin.claude-settings`: the permission rules and hooks in the ngrok Mac's Claude Code settings |
| `agent-plan` (`cmd/agent-plan`, `internal/plans`) | `nix build --no-link .#checks.aarch64-darwin.agent-plans`: emacs-config's plan-view tests against this agent-plan; its comment in `flake.nix` says how to point it at an emacs-config checkout |
| `modules/home-manager`, skills, `modules/home-manager/llm/AGENTS.md` | `nix build --no-link --print-out-paths .#darwinConfigurations.ngrok.config.home-manager.users.wgn.home-files` and read the files under the result, e.g. `.claude/CLAUDE.md` or `.claude/skills/<name>/SKILL.md`; and `nix build --no-link --dry-run .#homeConfigurations.devbox.activationPackage` |
| Claude Code settings, which activation merges into `~/.claude/settings.json` rather than linking | `nix eval --json .#darwinConfigurations.ngrok.config.home-manager.users.wgn.wgn.home.claude.settings`; for devbox, `.#homeConfigurations.devbox.config.wgn.home.claude.settings` |
| `modules/darwin`, `machines/darwin`, `overlays`, `flake.nix`, `flake.lock` | `nix build --no-link .#darwinConfigurations.ngrok.system`, which also builds agent-tools and runs its tests in the Nix sandbox |
| a `.nix` file | `nix fmt -- <file>` formats the whole file; commit only the formatting of lines you changed |

Run the checks by name: `nix flake check` also evaluates the NixOS machines,
which needs an x86_64-linux builder.

## Tests in `overlays/agent-tools`

The `write-tests` skill has the rules. In Go they take these forms:

- Related cases sit under one parent test with a short name (`TestParse`),
  each a `t.Run` subtest, so `-run TestParse/empty` selects one.
- Helpers call `t.Helper()`, so a failure points at the caller's line.
- Contexts come from `t.Context()`, which is cancelled when the test ends.
- Files go under `t.TempDir()`, and teardown in `t.Cleanup`.
- A package's `TestMain` calls `gitenv.Isolate()` before `m.Run()`, as
  `internal/gitenv/testmain_test.go` does; `gitenv` says what it isolates.
