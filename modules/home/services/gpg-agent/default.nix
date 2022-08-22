inputs@
{ config
, lib
, pkgs
, ...
}:

{
  services.gpg-agent = {
    enable = true;
    enableSshSupport = true;

    sshKeys = [
      "E1A99D519849CB5FE1C1AE4D88B2AA7DD529E17D"
    ];
  };
}
