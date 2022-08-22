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
    ];

    casks = [
      "hammerspoon"
      "reactotron"
      "virtualbox"
    ];
  };
}
