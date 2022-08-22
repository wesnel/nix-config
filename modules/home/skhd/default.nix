inputs@
{ pkgs
, lib
, brewPrefix
, ...
}:

let
  package = lib.strings.removeSuffix "/bin" brewPrefix;
  yabai = "${package}/bin/yabai";
in {
  home.file.skhdrc = {
    target = ".skhdrc";
    executable = true;

    text = ''
      # focus window
      alt - h : ${yabai} -m window --focus west
      alt - j : ${yabai} -m window --focus south
      alt - k : ${yabai} -m window --focus north
      alt - l : ${yabai} -m window --focus east
    '';
  };
}
