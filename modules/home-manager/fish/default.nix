{ pkgs
, ... }:

{
  imports = [
    ../../../components/fish
  ];

  home.shellAliases = let
    e = "$EDITOR";
    ls = "${pkgs.exa}/bin/exa";
    l = "${ls} -laF --color=always --color-scale --icons --git";
    tree = "${ls} -T";
    t = "${l} -T";
  in {
    inherit
      e
      ls
      l
      tree
      t;
  };
}
