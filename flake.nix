{
  description = "Wesley's Nix Configurations";

  inputs = {
    nix-darwin = {
      url = github:lnl7/nix-darwin;
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-config = {
      url = "git+https://git.sr.ht/~wgn/emacs-config";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    home-manager = {
      url = github:nix-community/home-manager;
      inputs.nixpkgs.follows = "nixpkgs";
    };

    nixpkgs = {
      url = github:nixos/nixpkgs/nixos-unstable;
    };
  };

  outputs =
    { self
    , nix-darwin
    , emacs-config
    , home-manager
    , nixpkgs }:

    let
      overlays =
        { system }:

        import ./overlays
          {
            emacs = emacs-config.defaultPackage.${system};
          };

      buildDarwinConfiguration =
        args@{ computerName
             , username
             , homeDirectory
             , system }:

        darwinModules:

        homeManagerModules:

        nix-darwin.lib.darwinSystem {
          inherit
            system;

          modules =
            darwinModules ++ [
              (_: { system.stateVersion = 4; })

              (_: {
                nixpkgs.overlays = overlays {
                  inherit
                    system;
                };
              })
            ] ++ [
              home-manager.darwinModules.home-manager {
                home-manager = {
                  users = {
                    "${username}" = _:

                      {
                        home.stateVersion = "22.05";
                        programs.home-manager.enable = true;
                        imports = homeManagerModules;
                      };
                  };

                  extraSpecialArgs = args;
                  useUserPackages = true;
                  useGlobalPkgs = true;
                  verbose = true;
                };
              }
            ];

          specialArgs = args;
        };

      darwinSystems = import ./machines/darwin { };

      darwinConfigurations = let
        op =
          _:

          { computerName
          , username
          , homeDirectory
          , system
          , homeManagerModules ? [ ]
          , darwinModules ? [ ] }:

          buildDarwinConfiguration
            {
              inherit
                computerName
                username
                homeDirectory
                system;
            }
            darwinModules
            homeManagerModules;
      in (builtins.mapAttrs op darwinSystems);
    in {
      inherit
        darwinConfigurations;
    };
}
