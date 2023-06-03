_:

{
  imports = [
    ../../../components/nix
  ];

  services = {
    nix-daemon = {
      enable = true;
    };
  };
}
