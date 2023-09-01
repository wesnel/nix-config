{ pkgs
, ... }:

{
  imports = [
    ../../../components/emacs
  ];

  environment = {
    pathsToLink = [
      "/share/hunspell"
    ];

    systemPackages = (with pkgs; [
      emacs
      enchant2
      nuspell
    ]) ++ (with pkgs.hunspellDicts; [
      en-us-large
    ]);
  };

  services.emacs = {
    enable = true;
  };
}
