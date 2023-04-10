{ pkgs
, ... }:

{
  nix = {
    package = pkgs.nixFlakes;

    extraOptions = ''
      experimental-features = nix-command flakes
      show-trace = true
    '';
  };

  services.nix-daemon = {
    enable = true;
  };
}
