{ homeDirectory
, username
, ...
}:

{
  environment.etc."paths".text = ''
    ${homeDirectory}/.nix-profile/bin
    /etc/profiles/per-user/${username}/bin
    /run/current-system/sw/bin
    /nix/var/nix/profiles/default/bin
    /usr/local/bin
    /usr/bin
    /usr/sbin
    /bin
    /sbin
  '';
}
