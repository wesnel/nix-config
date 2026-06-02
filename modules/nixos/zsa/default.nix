{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.zsa;
in {
  options.wgn.nixos.zsa = {
    enable = mkEnableOption "Enables my ZSA setup for NixOS";
  };

  config = mkIf cfg.enable {
    hardware.keyboard.zsa.enable = true;
  };
}
