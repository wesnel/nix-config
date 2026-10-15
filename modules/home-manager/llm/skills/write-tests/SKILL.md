---
name: write-tests
description: Checklist for tests that can fail and say why. Load before writing, changing or reviewing tests in any language, and before any change in behaviour (a feature, a fix, a changed contract), which starts with its test.
---

# Tests that can fail

A test is worth its upkeep only if some plausible change to the code turns it
red, and the failure points at what broke. Everything below follows from that.

## Before writing one

- Name the behaviour under test in one sentence a caller would recognise:
  "an expired token is rejected", not "`validate` calls `checkExpiry`".
- Name the one-line change to the code that would make the test fail. If
  there is none, the test checks nothing; write a different one.
- Find the existing tests for the same code and follow their layout, helpers
  and naming. Extend a table of cases before starting a new file.

## What to assert

- Assert what a caller can observe: return values, errors, output, state
  read back through the public interface. Private fields, call order and
  intermediate helpers change without the behaviour changing.
- Never compare a value with a copy of itself: an expected value computed by
  the code under test, a constant imported from it, or a fixture the code
  merely echoes back. Write the expected value out literally.
- Check an error by the property callers branch on (its kind, its code), not
  by its full message, unless the message is the contract.
- A snapshot of a whole output fits only when the whole output is the
  contract. Otherwise assert the parts that matter.
- Assert enough to tell a right answer from a wrong one. A test that checks
  only "no error" or "not empty" passes for most broken code.
- Assert an absence (no output, no warning, a field left out) together with
  its presence on another input, so the test shows the check would see it.

## Choosing cases

- One case per distinct behaviour or boundary: empty, one, many; just inside
  and just outside a limit; the first failure path. Inputs that exercise the
  same path add run time, not coverage.
- Name each case after the behaviour, so a failure reads as a sentence.
- Do not test the language, the standard library or a dependency. Test what
  this code does with them.

## The real thing first

- Use the real dependency wherever the test can run it: the real library, the
  real binary, a temporary directory, a git repository the test makes, an
  `httptest` server, a test Emacs, a real VM.
- Fake only what the test cannot run, or must not: a service that costs money
  or reaches outside the machine (GitHub, mail, payments), a clock or random
  source the result depends on, a failure the real thing will not produce on
  demand. A comment at the fake says which of these it is.
- A real dependency that is slow or needs an environment the default run
  lacks goes behind a switch the test names (an environment variable or a
  build tag), and runs in CI or, where CI cannot, is run once and named in
  the report. Skipped, it says how to run it; it never passes silently.
- Where two parts of our own meet, such as a CLI and the wrapper it calls, at
  least one test runs both real sides. If each side is tested only against a
  fake of the other, a mismatched contract passes every test.
- A fake that stays is held to the real thing: the same contract tests run
  against both, or at least it fails the way the real thing fails and
  returns data shaped like the real thing's.
- Give each external operation the code needs its own function, passed in
  rather than built inside, so a fake returns one shape with no branching on
  what it was asked.
- Never assert that a fake received what the test handed it. That checks
  the test's own wiring. Assert what the code did with the answer.

## Hermetic

- A test touches nothing outside what it set up: only its own temporary
  directories, never the home directory, global or system configuration,
  or the test binary itself. Point every path the code reads from the
  environment (`HOME`, `XDG_*`, a config path) into the test's directory.
- No internet. A network dependency gets a local stand-in the test starts,
  such as an `httptest` server, with its address injected into the code.
- Never rely on something being unreachable or absent: a closed port, a
  missing binary, a host with no route. Start the refusing server, or
  inject the missing one, so the test controls it.
- Fail closed: the test binary's setup points `HTTP_PROXY` and
  `HTTPS_PROXY` at a server of its own that refuses every connection, with
  `NO_PROXY` naming localhost, so a request that escapes the stand-ins fails
  instead of reaching the internet. In Go this is `TestMain`.
- When reviewing, a test that reaches the network, writes outside what it
  set up, or passes only because something is unreachable or absent is a
  finding.

## Test first

- Every change in behaviour (a feature, a fix, a changed contract) starts
  with its test. Write or change the test, run it against the unchanged code,
  and see it fail for the reason you expect: an assertion about the missing
  behaviour, not a compile error or a missing fixture. Then change the code
  until it passes.
- Work in vertical slices: one test, then the code that passes it, then the
  next test. Each slice shapes the next, which tests written all at once
  ahead of the code cannot.
- Keep both runs as evidence: the failing output, or its key lines, and the
  passing run. Both go in your final report and in what you hand a
  coordinator. A pull request description still names only what a reviewer
  cannot see from CI.
- The failing stage is never committed. The test lands in the same commit as
  the code it covers.
- Once committed, show the red run again with `git red-check` where it is
  installed: `git red-check` for HEAD, `git red-check --fixups BASE..HEAD`
  for a branch's fixups. In a throwaway worktree it runs the tests without
  the commit's other changes, where they must fail, and with them, where
  they must pass. Exit 0 is that evidence. Exit 3 means the tests pass
  without the change or fail with it: fix the tests. Exit 4 means the
  evidence is missing or weak (no test files changed, only test files
  changed, or the tests failed without the change only to build, or with no
  build command to say whether they built): say which in the report rather
  than claim a red run. Where Wesley has configured no settings for the
  repository, pass `--test`, `--build` and `--test-files`.
- For a bug, write the test from the symptom the report describes (the wrong
  output, the crash, the lost write), not from the line that changed. The
  next regression may come from somewhere else.
- A pure refactor changes no tests: the existing ones pass before and after,
  unchanged. If one has to change, the behaviour changed, and this section
  applies.
- Tests a refactor made obsolete, such as those of a module it removed, are
  deleted in a commit of their own, whose body names the tests that now
  cover that behaviour.
- Docs, configuration with nothing to test, and exploration that is not
  committed need no test first.

## Timing, order and isolation

- Never wait with a sleep. Wait on a signal the code gives when it is done:
  a channel, a callback, a hook, an event in its output. If it gives none,
  add one; a test that needs it is a caller that needs it. Drive time with an
  injected clock.
- Each test sets up what it needs and leaves nothing behind. None depends on
  another having run first; they pass alone, in any order and in parallel.
  Share setup only when it is too slow to repeat and read-only once built,
  and say so where it is defined.
- Seed or inject every source of randomness, time and environment the
  result depends on.

## Before finishing

- For each new or changed test that did not fail first, make the one-line
  change you named, run the test, and see it fail for the reason you
  expected. Then restore the code. A test that passes on its first run has
  not yet shown it can fail.
- While working, run the fast checks after each step: the build, the
  vetter or linter, the package's own tests. Run the whole suite at the end,
  not only the new tests.
- When reviewing someone else's tests, apply the same checks and name any
  test that cannot fail, with the change it misses.
