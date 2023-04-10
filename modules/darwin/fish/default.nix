{ username
, pkgs
, config
, ... }:

{
  imports = [
    ../../../components/fish
  ];

  environment = with pkgs; {
    loginShell = fish;
    shells = [ fish ];
  };

  users.users.${username} = {
    shell = config.programs.fish.package;
  };
}
