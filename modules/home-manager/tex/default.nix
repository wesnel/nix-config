{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.tex;
in {
  options.wgn.home.tex = {
    enable = mkEnableOption "Enables my TeX setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.texlive = {
      enable = true;

      extraPackages = tpkgs: {
        inherit
          (tpkgs)
          scheme-full
          ;
      };
    };
  };
}
