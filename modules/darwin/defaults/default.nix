_:

{
  system.defaults = {
    NSGlobalDomain = {
      AppleShowAllExtensions               = true;
      AppleShowAllFiles                    = true;
      AppleShowScrollBars                  = "Always";
      NSAutomaticCapitalizationEnabled     = false;
      NSAutomaticDashSubstitutionEnabled   = false;
      NSAutomaticPeriodSubstitutionEnabled = false;
      NSAutomaticQuoteSubstitutionEnabled  = false;
      NSAutomaticSpellingCorrectionEnabled = false;
      "com.apple.sound.beep.volume"        = 0.0;
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

  system.keyboard = {
    enableKeyMapping = true;
    remapCapsLockToControl = true;
  };
}
