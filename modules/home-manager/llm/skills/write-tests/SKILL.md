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

## Choosing cases

- One case per distinct behaviour or boundary: empty, one, many; just inside
  and just outside a limit; the first failure path. Inputs that exercise the
  same path add run time, not coverage.
- Name each case after the behaviour, so a failure reads as a sentence.
- Do not test the language, the standard library or a dependency. Test what
  this code does with them.

## Fakes

- Fake at the boundary where data enters or leaves: the store, the network,
  the clock, the filesystem, randomness. Run real code everywhere inside it.
- Where a real stand-in runs locally, use it rather than a fake: a
  temporary directory, a git repository made by the test, `httptest`, a test
  Emacs.
- Give each external operation the code needs its own function, passed in
  rather than built inside, so its fake returns one shape with no branching
  on what it was asked.
- Never assert that a fake received what the test handed it. That checks
  the test's own wiring. Assert what the code did with the fake's answer.
- Keep a fake faithful where it matters: it fails the way the real thing
  fails, and returns data shaped like the real thing's.

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

- Never wait with a sleep. Wait on the event itself, or drive a fake clock.
- Each test sets up what it needs and leaves nothing behind. None depends on
  another having run first; they pass alone, in any order and in parallel.
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
