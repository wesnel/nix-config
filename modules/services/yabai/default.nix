inputs@
{ pkgs
, lib
, brewPrefix
, username
, ...
}:

# HACK: i do some config here, but i use the homebrew-installed copy of yabai,
#       since it is more up-to-date. also see `../../home/yabai` for more.
let
  package = lib.strings.removeSuffix "/bin" brewPrefix;
  yabai = "${package}/bin/yabai";
in {
  homebrew = let
    tap = "koekeishiya/formulae";
    cask = "yabai";
  in {
    taps = [ tap ];

    extraConfig = ''
      brew "${tap}/${cask}", restart_service: true
    '';
  };

  services.yabai = {
    enable = false;
    enableScriptingAddition = true;

    inherit package;
  };

  environment.etc.yabai = {
    target = "sudoers.d/yabai";
    text = "${username} ALL = (root) NOPASSWD: ${yabai} --load-sa";
  };

  launchd.user.agents.yabai = {
    serviceConfig.ProgramArguments = [ yabai ];
    serviceConfig.KeepAlive = true;
    serviceConfig.RunAtLoad = true;
  };
}
