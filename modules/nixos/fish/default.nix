{ pkgs
, username
, ... }:

{
  imports = [
    ../../../components/fish
  ];

  users = {
    defaultUserShell = "${pkgs.fish}/bin/fish";
    users.${username}.shell = "${pkgs.fish}/bin/fish";
  };
}
