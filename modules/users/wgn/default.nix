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
      "networkmanager"
      "wheel"
    ];
  };
}
