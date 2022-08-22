inputs@
{ config
, lib
, pkgs
, ...
}:

{
  programs.password-store = {
    enable = true;

    package = pkgs.pass.withExtensions (exts: with exts; [
      pass-update
    ]);

    settings = {
      PASSWORD_STORE_KEY = "0xC9F55C247EBA37F4!";
      PASSWORD_STORE_SIGNING_KEY = "0x8AB4F50FF6C15D42!";
    };
  };

  home.packages = with pkgs; [
    ripasso-cursive
  ];
}
