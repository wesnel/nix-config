inputs@
{ config
, lib
, pkgs
, ...
}:

{
  services.mullvad-vpn = {
    enable = true;
  };

  environment.systemPackages = with pkgs; [
    mullvad
    mullvad-vpn
  ];
}
