{ username
, homeDirectory
, ... }:

{
  imports = [
    ../../../components/users
  ];

  users = {
    users.${username} = {
      isNormalUser = true;
      home = homeDirectory;
      createHome = true;

      extraGroups = [
        "audio"
        "disk"
        "networkmanager"
        "sway"
        "video"
        "wheel"
      ];
    };
  };
}
