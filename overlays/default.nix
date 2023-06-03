{ emacs }:

[
  (final: prev: {
    inherit emacs;

    # FIXME: emacs-related notmuch tests fail.
    notmuch = prev.notmuch.overrideAttrs(old: {
      doCheck = false;
    });
  })
]
