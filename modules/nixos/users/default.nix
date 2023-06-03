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
      home = "/home/${username}";
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
