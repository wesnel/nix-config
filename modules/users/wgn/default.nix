inputs@
{ pkgs
, username
, ...
}:

{
  users.users.${username} = {
    isNormalUser = true;
    shell = pkgs.fish;

    extraGroups = [
      "docker"
      "networkmanager"
      "wheel"
    ];
  };
}
