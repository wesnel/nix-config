# nix-config

`flake.nix` wires everything. Machines in `machines/{darwin,nixos,home-manager}/<name>`
enable modules from `modules/{darwin,nixos,home-manager}/<name>`, whose options
sit under `wgn.darwin`, `wgn.nixos` and `wgn.home`. Packages are in `overlays/`;
the agent tooling is the Go module `overlays/agent-tools`. `ngrok` is the
aarch64-darwin work Mac; `devbox` is an x86_64-linux Home Manager machine.

## Checking a change

Before building or testing a change, read `CHECKS.md`: the checks each
changed path needs. Building is as far as a change goes here; switching a
machine is the operator's, with the commands in the README's "updating
systems".

## Tests in `overlays/agent-tools`

The `write-tests` skill has the rules. In Go they take these forms:

- Related cases sit under one parent test with a short name (`TestParse`),
  each a `t.Run` subtest, so `-run TestParse/empty` selects one.
- Helpers call `t.Helper()`, so a failure points at the caller's line.
- Contexts come from `t.Context()`, which is cancelled when the test ends.
- Files go under `t.TempDir()`, and teardown in `t.Cleanup`.
- A package's `TestMain` calls `gitenv.Isolate()` before `m.Run()`, as
  `internal/gitenv/testmain_test.go` does; `gitenv` says what it isolates.
