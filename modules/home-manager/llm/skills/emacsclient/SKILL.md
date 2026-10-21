---
name: emacsclient
description: 'Check, compile and test Emacs Lisp with a batch Emacs, emacs -Q --batch with a throwaway --init-directory: check-parens, byte compilation and ERT, each exiting non-zero on failure. Use for every elisp check; no Emacs daemon is reachable from a sandboxed command.'
tools: Bash
---

# Checking Emacs Lisp with a batch Emacs

A sandboxed command reaches no Emacs daemon, the operator's or one of its
own, so every check of Emacs Lisp runs in a batch Emacs that exits when it
is done: `emacs -Q --batch` with an init directory of its own. The guard
lets `emacs` run only that way.

## The directory

```sh
sh -c 'mktemp -d "${TMPDIR:-/tmp}/et.XXXX"'
```

Use the directory it prints, say `/tmp/claude-501/et.AbCd`, literally in
every later command: the shell keeps no variable from one command to the
next, and fish has no `${VAR:-default}`, hence the `sh -c`. Pass it as
`--init-directory`: `-Q` alone still keeps `~/.emacs.d` as
`user-emacs-directory`, where packages, tramp, auto-save lists and the
native-compilation cache would write. Trash the directory when the checks
are done.

Add an `-L` for each directory the code under test loads from, and an `-l`
for anything it needs loaded first; never `-l default`, the operator's
configuration. Wrap each run in `timeout`: code that prompts
(`read-string`, `y-or-n-p`) or loops would otherwise never return.

## Check that the right file loads

The `emacs` on `PATH` puts the operator's installed packages on
`load-path` even under `-Q`, so a missing or wrong `-L` silently tests the
installed copy instead of the one you changed:

```sh
timeout 30 emacs -Q --batch --init-directory=/tmp/claude-501/et.AbCd -L /path/to/lisp --eval '
(progn
  (require (quote foo))
  (princ (symbol-file (quote foo))))'
```

It must print a path in the directory you changed. `locate-library` names
only the file `load` would find first, which beside a newer `.el` can be a
stale `.elc`; set `load-prefer-newer` to `t` with `--eval` before the
`-l` of a file you edited.

## Check parentheses

```sh
timeout 30 emacs -Q --batch --init-directory=/tmp/claude-501/et.AbCd --eval '
(with-temp-buffer
  (insert-file-contents "/path/to/file.el")
  (emacs-lisp-mode)
  (check-parens))'
```

It exits 0 for a balanced file and non-zero, printing `Unmatched bracket
or quote`, for one that is not. Keep the `(emacs-lisp-mode)`: without its
syntax table `;` does not start a comment, so a `)` in a comment can
balance a form left open.

## Byte-compile

```sh
timeout 120 emacs -Q --batch --init-directory=/tmp/claude-501/et.AbCd -L /path/to/lisp --eval '
(progn
  (require (quote bytecomp))
  (let ((byte-compile-error-on-warn t)
        (byte-compile-dest-file-function
         (lambda (_) "/tmp/claude-501/et.AbCd/out.elc")))
    (unless (byte-compile-file "/path/to/file.el")
      (kill-emacs 1))))'
```

It exits 0 when the file compiled with no warnings, and 1 when it had an
error or a warning, which it prints on stderr. Keep the `require` before
the `let`: the variables bind dynamically only once `bytecomp` is loaded.
The `.elc` goes into the throwaway directory, never beside the source.

## Run ERT

```sh
timeout 300 emacs -Q --batch --init-directory=/tmp/claude-501/et.AbCd -L /path/to/lisp \
  -l /path/to/repo/tests/test-foo.el -f ert-run-tests-batch-and-exit
```

It exits 0 when every test passed and non-zero when one failed. A
repository with a runner, `tests/run-NAME.el` defining
`wgn-NAME-run-tests`, is run with `-l` of the runner and `--eval
'(wgn-NAME-run-tests)'` instead; a runner signals an error when a test
fails and when none ran, since no test run means the load found none.

## Rules

- `emacs` only as `emacs -Q --batch` (or `--script`) with
  `--init-directory`, never a daemon or an interactive Emacs.
- Write `--eval` elisp across indented lines inside single quotes, which
  pass it unchanged in bash and in fish as long as it contains no `'` and
  no `\`. Write `(quote x)` for `'x`. Elisp that needs either character
  goes in a file you `-l` instead.
- Opening a file for the operator needs their Emacs, which no sandboxed
  command reaches: give them the path, as the `file-links` skill says.
