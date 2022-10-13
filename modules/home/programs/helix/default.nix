inputs@
{ lib
, pkgs
, ...
}:

{
  programs.helix = {
    enable = true;

    settings = {
      theme = "onedark";
    };
  };

  # FIXME: make configurable with conflicting emacs options
  home.sessionVariables.EDITOR = "hx";
  programs.fish.interactiveShellInit = "set -gx EDITOR hx";
}
