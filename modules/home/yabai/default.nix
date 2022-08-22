inputs@
{ pkgs
, lib
, brewPrefix
, ...
}:

# HACK: i do some config here, but i do some other config in
#       `../../services/yabai`.
let
  package = lib.strings.removeSuffix "/bin" brewPrefix;
  yabai = "${package}/bin/yabai";
in {
  home.file.yabairc = {
    target = ".yabairc";
    executable = true;

    text = let
      load-sa = "sudo ${yabai} --load-sa";
    in ''
      ${load-sa}
      ${yabai} -m signal --add event=dock_did_restart action="${load-sa}"

      ${yabai} -m config layout                bsp
      ${yabai} -m config window_gap            10
      ${yabai} -m config bottom_padding        10
      ${yabai} -m config left_padding          10
      ${yabai} -m config right_padding         10
      ${yabai} -m config top_padding           10
      ${yabai} -m config mouse_modifier        fn
      ${yabai} -m config mouse_action1         move
      ${yabai} -m config mouse_action2         resize
      ${yabai} -m config focus_follows_mouse   off
      ${yabai} -m config mouse_follows_focus   off
      ${yabai} -m config window_placement      second_child
      ${yabai} -m config window_topmost        on
      ${yabai} -m config active_window_opacity 1.0
      ${yabai} -m config normal_window_opacity 0.9

      ${yabai} -m rule --add app='.*System Preferences.*' manage=off
      ${yabai} -m rule --add app='.*System Information.*' manage=off
      ${yabai} -m rule --add app='.*Pinentry Mac.*' manage=off
      ${yabai} -m rule --add app='.*OpenVPN Connect.*' manage=off
    '';
  };
}
