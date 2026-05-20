with import <nixpkgs> {}; let
  sops-nix = builtins.fetchTarball {
    url = "https://github.com/Mic92/sops-nix/archive/master.tar.gz";
  };
in
  mkShell {
    # HACK: This can be automatically created via age.generateKey:
    SOPS_AGE_KEY_FILE = "~/.config/sops-nix/key.txt";

    sopsPGPKeyDirs = [
      "${toString ./.}/keys/hosts"
      "${toString ./.}/keys/users"
    ];

    nativeBuildInputs = [
      (pkgs.callPackage sops-nix {}).sops-import-keys-hook
    ];
  }
