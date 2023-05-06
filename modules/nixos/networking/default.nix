_:

{
  imports = [
    ../../../components/networking
  ];

  networking = {
    networkmanager.enable = true;
    useDHCP = false;

    interfaces = {
      wlp166s0 = {
        useDHCP = true;
      };
    };
  };

  hardware = {
    bluetooth.enable = true;
  };

  time.timeZone = "America/Los_Angeles";
  i18n.defaultLocale = "en_US.utf8";
}
