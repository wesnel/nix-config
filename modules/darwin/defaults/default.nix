{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.defaults;
in {
  options.wgn.darwin.defaults = {
    enable = mkEnableOption "Enables my Darwin-related default preferences";
  };

  config = mkIf cfg.enable {
    # HACK: This line only exists because I installed nix in a way
    # that nix-darwin doesn't like.
    ids.gids.nixbld = 350;

    security.pam.services.sudo_local.touchIdAuth = true;

    system.defaults = {
      NSGlobalDomain = {
        AppleShowAllExtensions = true;
        AppleShowAllFiles = true;
        AppleShowScrollBars = "Always";
        NSAutomaticCapitalizationEnabled = false;
        NSAutomaticDashSubstitutionEnabled = false;
        NSAutomaticPeriodSubstitutionEnabled = false;
        NSAutomaticQuoteSubstitutionEnabled = false;
        NSAutomaticSpellingCorrectionEnabled = false;
        "com.apple.sound.beep.volume" = 0.0;
      };

      SoftwareUpdate.AutomaticallyInstallMacOSUpdates = false;

      finder = {
        AppleShowAllExtensions = true;
        AppleShowAllFiles = true;
      };
    };

    system.keyboard = {
      enableKeyMapping = true;
      remapCapsLockToControl = true;
    };
  };
}
