{
  description = "Wesley's Nix Configurations";

  inputs = {
    nix-darwin = {
      url = "github:lnl7/nix-darwin";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-config = {
      url = "git+https://git.sr.ht/~wgn/emacs-config?ref=main";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    mujmap = {
      url = "github:wesnel/mujmap/wesnel/add-darwin-to-flake";
    };

    nixos-hardware = {
      url = "github:nixos/nixos-hardware";
    };

    nixpkgs = {
      # NOTE: Some options for this (in increasing order of stability)
      #       could be:
      #
      # url = "github:nixos/nixpkgs/nixos-unstable";
      #
      # url = "github:nixos/nixpkgs/nixos-23.11";
      #
      url = "github:nixos/nixpkgs/nixos-unstable";
    };

    nur = {
      url = "github:nix-community/NUR";
    };
  };

  outputs =
    { self
    , nix-darwin
    , emacs-config
    , home-manager
    , mujmap
    , nixos-hardware
    , nixpkgs
    , nur }:

    let
      # yubikey
      key = "0xC9F55C247EBA37F4!";
      signingKey = "0x8AB4F50FF6C15D42!";

      overlays = let

        flakes = {
          inherit
            emacs-config
            mujmap
            nur;
        };

      in
        { system }:

        import ./overlays
          {
            inherit
              flakes
              system;
          };

      buildNixosConfiguration =
        args@{ computerName
             , username
             , homeDirectory
             , key
             , signingKey
             , system }:

        nixosModules:

        homeManagerModules:

        nixpkgs.lib.nixosSystem {
          inherit
            system;

          modules =
            nixosModules ++ [
              ({ lib
               , ... }:

                {
                  system.stateVersion = lib.mkDefault "22.05";
                })

              (_:

                {
                  nixpkgs.overlays = overlays {
                    inherit
                      system;
                  };
                })

              (_:

                {
                  nixpkgs.config.allowUnfree = true;
                })
            ] ++ [
              home-manager.nixosModules.home-manager {
                home-manager = {
                  users = {
                    "${username}" = { lib
                                    , ... }:

                      {
                        home.stateVersion = lib.mkDefault "22.05";
                        programs.home-manager.enable = true;
                        imports = homeManagerModules;
                      };
                  };

                  backupFileExtension = "backup";
                  extraSpecialArgs = args;
                  useUserPackages = true;
                  useGlobalPkgs = true;
                  verbose = true;
                };
              }
            ];

          specialArgs = args;
        };

      nixosSystems = import ./machines/nixos {
        inherit
          emacs-config
          nixos-hardware;
      };

      nixosConfigurations = let
        op =
          _:

          { computerName
          , username
          , homeDirectory
          , system
          , homeManagerModules ? [ ]
          , nixosModules ? [ ] }:

          buildNixosConfiguration
            {
              inherit
                computerName
                username
                homeDirectory
                key
                signingKey
                system;
            }
            nixosModules
            homeManagerModules;
      in (builtins.mapAttrs op nixosSystems);

      buildDarwinConfiguration =
        args@{ computerName
             , username
             , homeDirectory
             , key
             , signingKey
             , system }:

        darwinModules:

        homeManagerModules:

        nix-darwin.lib.darwinSystem {
          inherit
            system;

          modules =
            darwinModules ++ [
              (_:

                {
                  system.stateVersion = 4;
                })

              (_:

                {
                  nixpkgs.overlays = overlays {
                    inherit
                      system;
                  };
                })
            ] ++ [
              home-manager.darwinModules.home-manager {
                home-manager = {
                  users = {
                    "${username}" = { lib
                                    , ... }:

                      {
                        home.stateVersion = lib.mkDefault "22.05";
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

      darwinSystems = import ./machines/darwin {
        inherit
          emacs-config;
      };

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
                key
                signingKey
                system;
            }
            darwinModules
            homeManagerModules;
      in (builtins.mapAttrs op darwinSystems);
    in {
      inherit
        darwinConfigurations
        nixosConfigurations;
    };
}
