inputs@
{ pkgs
, system
, username
, homeDirectory
, ...
}:

{
  imports = [
    ../../modules/etc/paths
    ../../modules/fonts
    ../../modules/homebrew
    ../../modules/nix
    ../../modules/programs/bash
    ../../modules/programs/fish
    ../../modules/programs/gnupg
    ../../modules/programs/zsh
    ../../modules/services/nix-daemon
    ../../modules/services/skhd
    (import ../../modules/services/yabai inputs)
  ];

  networking = let
    computerName = "wgn-shipt";
  in {
    inherit computerName;

    hostName = computerName;
  };

  environment = {
    loginShell = pkgs.fish;

    shells = with pkgs; [
      fish
      zsh
    ];
  };

  system = {
    defaults = {
      NSGlobalDomain = {
        AppleShowAllExtensions               = true;
        AppleShowAllFiles                    = true;
        AppleShowScrollBars                  = "Always";
        NSAutomaticCapitalizationEnabled     = false;
        NSAutomaticDashSubstitutionEnabled   = false;
        NSAutomaticPeriodSubstitutionEnabled = false;
        NSAutomaticQuoteSubstitutionEnabled  = false;
        NSAutomaticSpellingCorrectionEnabled = false;
        "com.apple.sound.beep.volume"        = "0.0";
      };

      SoftwareUpdate.AutomaticallyInstallMacOSUpdates = false;

      finder = {
        AppleShowAllExtensions = true;
        AppleShowAllFiles      = true;
      };

      universalaccess = {
        reduceTransparency = false;
      };
    };

    keyboard = {
      enableKeyMapping = true;
      remapCapsLockToControl = true;
    };

    stateVersion = 4;
  };

  users = {
    users.${username} = {
      home = homeDirectory;
    };
  };
}
