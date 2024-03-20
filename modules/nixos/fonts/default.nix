_:

{
  imports = [
    ../../../components/fonts
  ];

  fonts = {
    enableDefaultPackages = true;

    fontconfig = {
      defaultFonts = {
        monospace = [
          "Iosevka Term"
        ];
      };
    };
  };
}
