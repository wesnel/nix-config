inputs@
{ config
, lib
, pkgs
, ...
}:

{
  programs.kitty = {
    enable = true;
    theme = "Modus Vivendi";

    settings = {
      "macos_option_as_alt" = true;
      "macos_thicken_font" = "0.75";
      "scrollback_pager_history_size" = 100;
      "scrollback_fill_enlarged_window" = true;

      # NOTE: Re-add keyboard shortcuts as needed.
      "clear_all_shortcuts" = true;
    };

    font = {
      name = "FiraCode Nerd Font";
      package = pkgs.nerdfonts;
      size = 10;
    };

    keybindings = {
      # https://sw.kovidgoyal.net/kitty/conf/#keyboard-shortcuts
      "ctrl+c" = "copy_or_interrupt";
      "cmd+c" = "copy_to_clipboard";

      "ctrl+v" = "paste_from_clipboard";
      "cmd+v" = "paste_from_clipboard";

      "ctrl+shift+up" = "scroll_line_up";
      "ctrl+shift+k" = "scroll_line_up";
      "cmd+up" = "scroll_line_up";

      "ctrl+shift+down" = "scroll_line_down";
      "ctrl+shift+j" = "scroll_line_down";
      "cmd+down" = "scroll_line_down";

      "ctrl+shift+page_up" = "scroll_page_up";
      "cmd+page_up" = "scroll_page_up";

      "ctrl+shift+page_down" = "scroll_page_down";
      "cmd+page_down" = "scroll_page_down";

      "ctrl+shift+home" = "scroll_home";
      "cmd+home" = "scroll_home";

      "ctrl+shift+end" = "scroll_end";
      "cmd+end" = "scroll_end";

      "ctrl+shift+h" = "show_scrollback";
      "ctrl+shift+g" = "show_last_command_output";

      "ctrl+shift+enter" = "new_window";
      "cmd+enter" = "new_window";

      "ctrl+shift+w" = "close_window";
      "shift+cmd+d" = "close_window";

      "ctrl+shift+]" = "next_window";
      "ctrl+shift+[" = "previous_window";
      "ctrl+shift+f7" = "focus_visible_window";

      "ctrl+shift+f" = "move_window_forward";
      "ctrl+shift+b" = "move_window_backward";
      "ctrl+shift+`" = "move_window_to_top";
      "ctrl+shift+f8" = "swap_with_window";

      "ctrl+shift+r" = "start_resizing_window";
      "cmd+r" = "start_resizing_window";

      "ctrl+shift+n" = "new_os_window";
      "cmd+n" = "new_os_window";

      "ctrl+tab" = "next_tab";
      "ctrl+shift+tab" = "previous_tab";

      "ctrl+shift+t" = "new_tab";
      "cmd+t" = "new_tab";

      "ctrl+shift+q" = "close_tab";
      "cmd+w" = "close_tab";

      "ctrl+shift+." = "move_tab_forward";
      "ctrl+shift+," = "move_tab_backward";

      "ctrl+shift+l" = "next_layout";

      "ctrl+equal" = "change_font_size all +2.0";
      "cmd+equal" = "change_font_size all +2.0";

      "ctrl+minus" = "change_font_size all -2.0";
      "cmd+minus" = "change_font_size all -2.0";

      "ctrl+shift+f1" = "show_kitty_doc overview";

      "ctrl+shift+f11" = "toggle_fullscreen";
      "ctrl+cmd+f" = "toggle_fullscreen";

      "ctrl+shift+f10" = "toggle_maximized";

      "opt+cmd+s" = "toggle_macos_secure_keyboard_entry";

      "ctrl+shift+f6" = "debug_config";
      "opt+cmd+," = "debug_config";
    };
  };

  home.packages = with pkgs; [
    imagemagick
  ];
}
