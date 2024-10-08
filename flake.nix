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

    flake-utils = {
      url = "github:numtide/flake-utils";
    };

    home-manager = {
      # NOTE: Some options for this (in increasing order of stability)
      #       could be:
      #
      url = "github:nix-community/home-manager";
      #
      # url = "github:nix-community/home-manager/release-24.05";
      #
      inputs.nixpkgs.follows = "nixpkgs";
    };

    mujmap = {
      url = "github:wesnel/mujmap/wesnel/add-darwin-to-flake";
    };

    nixos-hardware = {
      url = "github:nixos/nixos-hardware/master";
    };

    nixpkgs = {
      # NOTE: Some options for this (in increasing order of stability)
      #       could be:
      #
      # url = "github:nixos/nixpkgs/master";
      #
      url = "github:nixos/nixpkgs/nixos-unstable";
      #
      # url = "github:nixos/nixpkgs/nixos-24.05";
    };

    nur = {
      url = "github:nix-community/NUR";
    };
  };

  outputs = {
    self,
    nix-darwin,
    emacs-config,
    flake-utils,
    home-manager,
    mujmap,
    nixos-hardware,
    nixpkgs,
    nur,
  }: let
    # yubikey
    key = "0xC9F55C247EBA37F4!";
    signingKey = "0x8AB4F50FF6C15D42!";

    overlays = let
      flakes = {
        inherit
          emacs-config
          mujmap
          nur
          ;
      };
    in
      {system}:
        import ./overlays
        {
          inherit
            flakes
            system
            ;
        };

    homeManagerModules = [
      ./modules/home-manager/emacs
      ./modules/home-manager/email
      ./modules/home-manager/firefox
      ./modules/home-manager/fish
      ./modules/home-manager/foot
      ./modules/home-manager/gamedev
      ./modules/home-manager/git
      ./modules/home-manager/gnupg
      ./modules/home-manager/man
      ./modules/home-manager/music
      ./modules/home-manager/pass
      ./modules/home-manager/photos
      ./modules/home-manager/sway
      ./modules/home-manager/video
      ./modules/home-manager/virtualisation
      ./modules/home-manager/yubikey
    ];

    buildNixosConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      signingKey,
      system,
      extraNixOSModules,
      extraHomeManagerModules,
    }: nixosModules: homeManagerModules:
      nixpkgs.lib.nixosSystem {
        inherit
          system
          ;

        modules =
          nixosModules
          ++ extraNixOSModules
          ++ [
            ({lib, ...}: {
              system.stateVersion = lib.mkDefault "22.05";
            })

            (_: {
              nixpkgs.overlays = overlays {
                inherit
                  system
                  ;
              };
            })

            (_: {
              nixpkgs.config.allowUnfree = true;
            })
          ]
          ++ [
            home-manager.nixosModules.home-manager
            {
              home-manager = {
                users = {
                  "${username}" = {lib, ...}: {
                    home.stateVersion = lib.mkDefault "22.05";
                    programs.home-manager.enable = true;
                    imports = homeManagerModules ++ extraHomeManagerModules;
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
        nixos-hardware
        ;
    };

    nixosConfigurations = let
      op = _: {
        computerName,
        username,
        homeDirectory,
        system,
        extraNixOSModules,
        extraHomeManagerModules,
      }:
        buildNixosConfiguration
        {
          inherit
            computerName
            username
            homeDirectory
            key
            signingKey
            system
            extraNixOSModules
            extraHomeManagerModules
            ;
        }
        nixosModules
        homeManagerModules;
    in (builtins.mapAttrs op nixosSystems);

    nixosModules = [
      ./modules/nixos/emacs
      ./modules/nixos/fish
      ./modules/nixos/fonts
      ./modules/nixos/interception-tools
      ./modules/nixos/kde
      ./modules/nixos/mullvad
      ./modules/nixos/networking
      ./modules/nixos/nix
      ./modules/nixos/sddm
      ./modules/nixos/steam
      ./modules/nixos/users
      ./modules/nixos/virtualisation
      ./modules/nixos/wayland
      ./modules/nixos/yubikey
    ];

    buildDarwinConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      signingKey,
      system,
      extraHomeManagerModules,
      extraDarwinModules,
    }: darwinModules: homeManagerModules:
      nix-darwin.lib.darwinSystem {
        inherit
          system
          ;

        modules =
          darwinModules
          ++ extraDarwinModules
          ++ [
            (_: {
              system.stateVersion = 4;
            })

            (_: {
              nixpkgs.overlays = overlays {
                inherit
                  system
                  ;
              };
            })
          ]
          ++ [
            home-manager.darwinModules.home-manager
            {
              home-manager = {
                users = {
                  "${username}" = {lib, ...}: {
                    home.stateVersion = lib.mkDefault "22.05";
                    programs.home-manager.enable = true;
                    imports = homeManagerModules ++ extraHomeManagerModules;
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
        emacs-config
        ;
    };

    darwinConfigurations = let
      op = _: {
        computerName,
        username,
        homeDirectory,
        system,
        extraHomeManagerModules,
        extraDarwinModules,
      }:
        buildDarwinConfiguration
        {
          inherit
            computerName
            username
            homeDirectory
            key
            signingKey
            system
            extraHomeManagerModules
            extraDarwinModules
            ;
        }
        darwinModules
        homeManagerModules;
    in (builtins.mapAttrs op darwinSystems);

    darwinModules = [
      ./modules/darwin/defaults
      ./modules/darwin/emacs
      ./modules/darwin/fish
      ./modules/darwin/fonts
      ./modules/darwin/gnupg
      ./modules/darwin/networking
      ./modules/darwin/nix
      ./modules/darwin/paths
      ./modules/darwin/users
      ./modules/darwin/yubikey
    ];
  in {
    inherit
      darwinConfigurations
      darwinModules
      homeManagerModules
      nixosConfigurations
      nixosModules
      ;

    formatter = flake-utils.lib.eachDefaultSystem (system: let
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      ${system} = pkgs.alejandra;
    });

    devShells = flake-utils.lib.eachDefaultSystem (system: let
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      default = pkgs.mkShell {
        buildInputs = with pkgs; [
          alejandra
          nil
        ];
      };
    });
  };
}
