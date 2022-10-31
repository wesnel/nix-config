inputs@
{ config
, lib
, pkgs
, ...
}:

with lib;

let
  cfg = config.environment;
in {
  imports = [ ../environment/etc/terminfo ];

  options.environment.enableAllTerminfo = with lib; mkOption {
    default = false;
    type = types.bool;
    description = lib.mdDoc ''
      Whether to install all terminfo outputs
    '';
  };

  config = mkIf cfg.enableAllTerminfo {
    environment = {
      variables = {
        TERMINFO_DIRS = (map (path: path + "/share/terminfo") cfg.profiles) ++
                        [ "/usr/share/terminfo"
                          # fixes `tput: unknown terminal "xterm-kitty"`
                          "${pkgs.kitty.terminfo}/share/terminfo"
                        ];
      };

      systemPackages = (map (x: x.terminfo) (with pkgs; [
        kitty
      ]));
    };
  };
}
