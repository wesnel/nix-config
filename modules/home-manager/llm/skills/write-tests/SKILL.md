---
name: write-tests
description: Checklist for tests that can fail and say why. Load before writing, changing or reviewing tests in any language, including a test that comes with a bug fix.
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
- Never assert that a fake received what the test handed it. That checks
  the test's own wiring. Assert what the code did with the fake's answer.
- Keep a fake faithful where it matters: it fails the way the real thing
  fails, and returns data shaped like the real thing's.

## Bug fixes

- Write the test first, from the symptom the bug report describes, and watch
  it fail against the unfixed code. Then fix the code and watch it pass.
- Assert the symptom (the wrong output, the crash, the lost write), not the
  line that changed. The next regression may come from somewhere else.

## Timing, order and isolation

- Never wait with a sleep. Wait on the event itself, or drive a fake clock.
- Each test sets up what it needs and leaves nothing behind. None depends on
  another having run first; they pass alone, in any order and in parallel.
- Seed or inject every source of randomness, time and environment the
  result depends on.

## Before finishing

- For each new or changed test, make the one-line change you named, run the
  test, and see it fail for the reason you expected. Then restore the code.
  A test that passes on its first run has not yet shown it can fail.
- Run the whole suite, not only the new tests.
- When reviewing someone else's tests, apply the same checks and name any
  test that cannot fail, with the change it misses.
