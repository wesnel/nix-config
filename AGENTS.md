# nix-config

`flake.nix` wires everything. Hosts in `machines/{darwin,nixos,home-manager}/<host>`
enable modules from `modules/{darwin,nixos,home-manager}/<name>` (options under
`wgn.darwin`, `wgn.nixos`, `wgn.home`). Packages are in `overlays/`; agent
tooling is the Go module `overlays/agent-tools`. `ngrok` is the aarch64-darwin
work Mac; `devbox` is an x86_64-linux home-manager host it deploys to.

The operator is the person this configuration belongs to, who signs, pushes and decides.

## Checking a change

Run from the repository root unless a directory is given. Nix sees only files
git tracks: `git add` a new file before building.

| Changed | Run |
| --- | --- |
| `overlays/agent-tools` | in that directory: `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`; then `nix build --no-link '.#darwinConfigurations.ngrok.pkgs.agent-tools'`, which runs the tests and the `postCheck` again in the sandbox |
| `modules/home-manager`, or skills and `modules/home-manager/llm/AGENTS.md` | `nix build --no-link --print-out-paths '.#darwinConfigurations.ngrok.config.home-manager.users.wgn.home-files'` and read the result, e.g. `<out>/.claude/CLAUDE.md`, `<out>/.claude/skills/<name>/SKILL.md`; also `nix build --dry-run '.#homeConfigurations.devbox.activationPackage'` |
| merged settings (`~/.claude/settings.json` and the like are written at activation, not linked) | `nix eval --json '.#darwinConfigurations.ngrok.config.home-manager.users.wgn.wgn.home.claude.settings'`; for devbox, `'.#homeConfigurations.devbox.config.wgn.home.claude.settings'` |
| `modules/darwin`, `machines/darwin` | `nix build --no-link '.#darwinConfigurations.ngrok.system'` |
| any `.nix` file | `nix fmt -- <files you changed>`. Main has files alejandra would reformat; leave them alone |

Tests that create git repositories must not sign: set `GIT_CONFIG_GLOBAL=/dev/null`
and `GIT_CONFIG_NOSYSTEM=1`, as the existing tests do.

### Go tests in `overlays/agent-tools`

- Group related cases under one parent test with a short name
  (`TestParse`), each case a `t.Run` subtest, so `-run TestParse/empty`
  selects one.
- Helpers call `t.Helper()`, so a failure points at the caller's line.
- Contexts come from `t.Context()`, never `context.Background()` or
  `context.TODO()`: it is cancelled when the test ends, which stops what the
  test started.
- Use `t.TempDir()` for files and `t.Cleanup` for teardown.
- Each test and subtest builds its own state. Share setup only when it is too
  slow to repeat, read-only once built, and made in `TestMain` or a
  `sync.OnceValue`; say so where it is defined.
- No sleeps: wait on a channel, callback or hook the code signals when it is
  done, adding one if it has none, or inject a clock.
- Hermetic: a test writes only under its own `t.TempDir()`, never the home
  directory, global configuration or the test binary, and reaches no
  network. A network dependency is an `httptest` server whose address the
  test injects. Every package's `TestMain` calls `gitenv.Isolate()`, so
  the package fails closed as `gitenv` documents; never rely on a port
  being closed or a binary missing.

## Never

- `darwin-rebuild switch`, `home-manager switch`, `nixos-rebuild`, `deploy`:
  give the operator the command.
- Decrypt, print or re-encrypt `secrets/wgn.yaml` (`sops`). Its key names are
  plaintext; the values are not yours to read.
