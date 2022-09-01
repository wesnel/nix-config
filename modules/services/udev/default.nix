inputs@
{ pkgs
, ...
}:

{
  services.udev = {
    packages = with pkgs; [
      yubikey-personalization
    ];
  };
}
