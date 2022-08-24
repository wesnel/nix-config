inputs@
{ pkgs
, ...
}:

{
  imports = [
    ../afew
    ../mbsync
  ];

  programs.notmuch = {
    enable = true;

    new = {
      tags = [ "new" ];
    };

    hooks = {
      preNew = ''
        mbsync --all
      '';

      postNew = ''
        afew -tn
      '';
    };
  };
}
