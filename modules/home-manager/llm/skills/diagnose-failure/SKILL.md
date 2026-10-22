---
name: diagnose-failure
description: Find the cause of a failure before changing code - a bug report, a failing or flaky test, a wrong output, a crash, a regression or a slowdown. Use when something fails and its cause is not yet shown.
---

# Diagnosing a failure

The cause comes from a reproduction, never from reading code first. Work
through the stages in order; skip one only by writing in the plan why it
does not apply.

## 1. A red command

Before forming any theory, build one command that fails on the symptom as
reported: the same wrong value, error or crash, not a similar one. It is
ready when:

- one command, rerun as it stands, shows the symptom, and its exit status
  says pass or fail;
- it fails every run, or at a rate you have measured;
- it takes seconds, so it can run after every probe.

Ways to build one, the first that fits:

- A test at the seam the report names: `go test -run 'TestName$' ./pkg`,
  or an ERT test in a test Emacs.
- The command on a fixture, its output compared with `diff` against the
  output expected.
- A good and a bad revision compared: `nix build` of each and `diff -r`,
  `nix eval` of the option at each, or `git bisect run CMD` between them.
- An input-shaped failure: `go test -fuzz`, seeded with the reported input.
- A flaky failure: raise its rate until it is reliable, with
  `go test -count=200 -race -cpu 1,4 -run ...` or under load, and record the
  rate.
- A slowdown: a baseline before anything else, with `go test -bench` at
  `-count=10` and `benchstat`, or `hyperfine`.

Log the command as the step's evidence. A secret it needs comes from the
environment by name, never written into the command, and a report quotes
only the output lines that show the symptom.

If no such command can be built, stop. Ask with `agent-plan ask` for what
would make one possible (access to an environment, a captured log or
request, or leave to add temporary logging), with what you tried as its
context.

## 2. Shrink it

Remove one input, caller, setting or step at a time, rerunning after each,
and keep a removal only while the command still fails the same way. Stop
when every part left is needed. Keep the original command as well: the fix
is checked against both.

## 3. Hypotheses

Before testing any, write three to five hypotheses under the step in the
plan, most likely first. Each predicts something the command can show: "if
X is the cause, changing Y makes it pass", or "makes it fail more often". A
hypothesis that predicts nothing is dropped.

## 4. Probes

- Each probe tests one prediction and changes one thing. Record its result
  against its hypothesis.
- Prefer a debugger or a REPL (`dlv`, `edebug`, `nix repl`) to logging.
  Where logging is needed, log at the boundaries that tell the hypotheses
  apart.
- Mark every temporary log line `DEBUG-` and four hex digits chosen for this
  diagnosis, so `git grep` on the marker finds them all. Nothing carrying
  the marker is committed.

## 5. The fix

- Its test fails before the fix for the reported reason, at a seam that
  reproduces the real call, as the `write-tests` skill says. Where no seam
  reaches the failure, that is a finding: record it in the plan and the
  hand-back instead of testing a shallower seam.
- After the fix, both the shrunken and the original command pass; for a
  flaky failure, give the rate over the same number of runs.
- `git grep` on the marker finds nothing, and throwaway files are deleted.
- The commit body names the cause: the hypothesis that held.
