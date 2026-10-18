---
name: emacsclient
description: 'Use emacsclient, never emacs --batch, for every Emacs operation. Check-parens, byte compilation, ERT and any other elisp evaluation go to a throwaway emacs -Q server you start in its own directory; the operator''s running Emacs is only for opening or showing what the operator asks to see.'
tools: Bash
---

# Emacs through emacsclient

Every Emacs operation goes through `emacsclient`, never `emacs --batch`.
There are two servers, and which one a command reaches matters:

- **A test server you start** for check-parens, byte compilation, ERT and
  any other elisp you evaluate to check or try something. It starts with
  `-Q` and its own `--init-directory`, so it runs none of the operator's
  init and writes nothing under their `~/.emacs.d`.
- **The operator's server** only to open or show something the operator
  asked to see, or for what another skill, such as `notify`, says to send
  there. A bare `emacsclient`, with no `-s`, reaches it.

## Open or show something for the operator

`emacsclient --no-wait /path/to/file` visits a file and returns at once;
put `+LINE` before the path to land on a line. Evaluate nothing there to
test, load or compile code.

## Check, compile and test on a test server

### Start it

```sh
mktemp -d /tmp/et.XXXX
```

Use the directory it prints, say `/tmp/et.AbCd`, literally in every later
command: the shell does not keep variables from one command to the next.
A long path does not start: macOS caps a socket path at 104 bytes, and on
macOS the Emacs daemon refuses a `--daemon` path of 63 bytes or more with
`daemon: child name too long`, so a scratchpad path is far too long.

```sh
timeout 60 emacs -Q --init-directory=/tmp/et.AbCd --eval '(setq load-prefer-newer t)' -L /path/to/lisp --daemon=/tmp/et.AbCd/server
timeout 30 emacsclient -s /tmp/et.AbCd/server --eval '(emacs-pid)'
```

- `--init-directory` with `-Q`: `-Q` alone still keeps `~/.emacs.d` as
  `user-emacs-directory`, where packages, tramp, auto-save lists and the
  native-compilation cache would write.
- `load-prefer-newer`: without it, a stale `.elc` beside an edited `.el`
  is what loads.
- Add an `-L` for each directory the code under test loads from, and an
  `-l` for anything it needs loaded first; never `-l default`, which is
  the operator's configuration.
- `timeout` on the start: an `-l` or `--eval` that loops would otherwise
  never let it return. It bounds only the start; the daemon keeps running
  once it is up. When the start times out, the daemon it forked is still
  running, stuck: stop it with `pkill -f /tmp/et.AbCd/server` before
  removing the directory.
- Note the pid the second command prints; the stop step may need it.

The `emacs` on `PATH` puts the operator's installed packages and their own
libraries on `load-path` even under `-Q`, so a missing or wrong `-L`
silently tests the installed copy instead of the one you changed. Before
trusting a result, load the feature and check which file it came from:

```sh
timeout 30 emacsclient -s /tmp/et.AbCd/server --eval '
(progn
  (require (quote foo))
  (symbol-file (quote foo)))'
```

It must print a path in the directory you changed. `symbol-file` is nil
until something has loaded the feature, hence the `require`; after a test
run, it names the file the tests used. `locate-library` shows only the
directory found: beside a newer `.el` it still prints the stale `.elc`,
even though `load-prefer-newer` makes the `.el` the one that loads.

That `emacs` may also be a different Emacs version from the operator's
running one; `emacs --version` names it, where a result depends on the
version.

Address the server by the full socket path with `-s` on every call, never
by a bare name, which resolves into the operator's own socket directory.
Wrap each call in `timeout`: code under test that prompts (`read-string`,
`y-or-n-p`) waits for an answer nobody gives, and the call never returns.

### Check parentheses

```sh
timeout 30 emacsclient -s /tmp/et.AbCd/server --eval '
(with-temp-buffer
  (insert-file-contents "/path/to/file.el")
  (emacs-lisp-mode)
  (check-parens)
  "balanced")'
```

An unbalanced file prints `*ERROR*: Unmatched bracket or quote` and exits 1.

### Byte-compile

```sh
timeout 120 emacsclient -s /tmp/et.AbCd/server --eval '
(progn
  (require (quote bytecomp))
  (with-current-buffer (get-buffer-create byte-compile-log-buffer)
    (let ((inhibit-read-only t)) (erase-buffer)))
  (let* ((byte-compile-dest-file-function
          (lambda (src)
            (expand-file-name (concat (file-name-nondirectory src) "c")
                              "/tmp/et.AbCd")))
         (ok (byte-compile-file "/path/to/file.el")))
    (with-current-buffer byte-compile-log-buffer
      (write-region nil nil "/tmp/et.AbCd/compile.log"))
    ok))'
```

It prints `t` when compilation succeeded and `nil` when it failed; read
`/tmp/et.AbCd/compile.log` for the warnings and errors. Return only short
values like this through `emacsclient`: a long result reaches the terminal
garbled, with the exit status still 0. The `.elc` goes into the test
directory rather than beside the source. Keep the `require` before the
`let*`: the variable binds dynamically only once `bytecomp` is loaded.

### Run ERT

```sh
timeout 300 emacsclient -s /tmp/et.AbCd/server --eval '
(progn
  (when (file-exists-p "/tmp/et.AbCd/ert.log")
    (delete-file "/tmp/et.AbCd/ert.log"))
  (require (quote ert))
  (load "/path/to/file-tests.el" nil t)
  (setq message-log-max t)
  (with-current-buffer (messages-buffer)
    (let ((inhibit-read-only t)) (erase-buffer)))
  (let ((stats (ert-run-tests-batch "^prefix-")))
    (with-current-buffer (messages-buffer)
      (write-region nil nil "/tmp/et.AbCd/ert.log"))
    (format "%d tests: %d expected, %d unexpected, %d skipped"
            (ert-stats-total stats)
            (ert-stats-completed-expected stats)
            (ert-stats-completed-unexpected stats)
            (ert-stats-skipped stats))))'
```

The run passed only when unexpected is 0 and the total is the number of
tests you meant to run: `0 tests` means the selector or the load found
none, which is a failure, not a pass. Read `/tmp/et.AbCd/ert.log` for
each test's result and the failures' conditions, and only after the call
printed that summary: when the test file fails to load or the call times
out, there is no summary and no log from this run. Never call `ert-run-tests-batch-and-exit` or anything else
that ends with `kill-emacs` mid-run; it takes the server down before the
result is printed.

### Stop it

```sh
timeout 10 emacsclient -s /tmp/et.AbCd/server --eval '(kill-emacs)' || kill 12345
trash /tmp/et.AbCd
```

with the pid noted at the start in place of `12345`; `kill` is for a
server wedged on a prompt, where `(kill-emacs)` never returns. Emacs
deletes the directory itself when only the socket was in it, and `trash`
then reports it missing. Remove the directory after a failed start too,
which leaves it empty. Where there is no `trash` command, as on Linux,
use `rm -r /tmp/et.AbCd`.

Start a fresh server for each round of checks rather than reusing one
into which earlier code was loaded.

## Rules

- `emacsclient`, never `emacs --batch`; `emacs` only as the start command
  above.
- When you check, compile, test or evaluate code, every call carries
  `-s <full socket path>` of a test server you started. Only opening or
  showing something for the operator, and what another skill says to send
  there, goes to the operator's server.
- Write `--eval` elisp across indented lines inside single quotes, which
  pass it unchanged in bash and in fish as long as it contains no `'` and
  no `\`. A `'` ends the quoted string in both shells, so write
  `(quote x)` for `'x`. Fish also reads `\\` and `\'` inside single quotes
  as escapes, where bash keeps every backslash. Elisp that needs either
  character goes in a file you `load` instead.
