inputs@
{ pkgs
, brewPrefix
, ...
}:

{
  homebrew = {
    enable = true;
    brewPrefix = "${brewPrefix}/bin";

    brews = [
      "adr-tools"
      "mas"
      "pkg-config"
      "poppler"
      "autoconf"
      "automake"
    ];

    casks = [
      "hammerspoon"
      "reactotron"
      "virtualbox"
    ];
  };
}
