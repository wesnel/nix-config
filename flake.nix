{
  description = "Wesley's Nix Configurations";

  inputs = {
    nix-darwin = {
      url = "github:lnl7/nix-darwin";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-config = {
      url = "github:wesnel/emacs-config";
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
      # url = "github:nix-community/home-manager/release-25.11";
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
      # url = "github:nixos/nixpkgs/nixos-25.11";
    };

    nixpkgs-stable = {
      url = "github:nixos/nixpkgs/nixos-25.11";
    };

    nur = {
      url = "github:nix-community/NUR";
    };

    sops-nix = {
      url = "github:Mic92/sops-nix";
      inputs.nixpkgs.follows = "nixpkgs";
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
    nixpkgs-stable,
    nur,
    sops-nix,
  }: let
    key = "0xA776D2AD099E8BC0";

    homeManagerModules = [
      sops-nix.homeManagerModules.sops

      (_: {
        sops.gnupg.sshKeyPaths = ["/etc/ssh/ssh_host_ed25519_key"];
      })

      ./modules/home-manager/copilot
      ./modules/home-manager/emacs
      ./modules/home-manager/firefox
      ./modules/home-manager/fish
      ./modules/home-manager/fonts
      ./modules/home-manager/foot
      ./modules/home-manager/gamedev
      ./modules/home-manager/gcloud
      ./modules/home-manager/git
      ./modules/home-manager/gnupg
      ./modules/home-manager/go
      ./modules/home-manager/hyprland
      ./modules/home-manager/man
      ./modules/home-manager/music
      ./modules/home-manager/ollama
      ./modules/home-manager/pass
      ./modules/home-manager/photos
      ./modules/home-manager/python
      ./modules/home-manager/sway
      ./modules/home-manager/video
      ./modules/home-manager/virtualisation
      ./modules/home-manager/yubikey
      ./modules/home-manager/zwift
    ];

    nixosModules = [
      sops-nix.nixosModules.sops

      (_: {
        sops.gnupg.sshKeyPaths = ["/etc/ssh/ssh_host_ed25519_key"];
      })

      ./modules/nixos/emacs
      ./modules/nixos/fish
      ./modules/nixos/fonts
      ./modules/nixos/hyprland
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
      ./modules/nixos/zwift
    ];

    darwinModules = [
      ./modules/darwin/defaults
      ./modules/darwin/emacs
      ./modules/darwin/fish
      ./modules/darwin/fonts
      ./modules/darwin/gnupg
      ./modules/darwin/homebrew
      ./modules/darwin/networking
      ./modules/darwin/nix
      ./modules/darwin/paths
      ./modules/darwin/users
      ./modules/darwin/yubikey
    ];

    buildNixosConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      system,
      extraNixOSModules,
      extraHomeManagerModules,
    }: nixosModules: homeManagerModules: overlay:
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
              nixpkgs.overlays = [
                nur.overlays.default
                emacs-config.overlays.default
                overlay
              ];
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

    buildDarwinConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      system,
      extraHomeManagerModules,
      extraDarwinModules,
    }: darwinModules: homeManagerModules: overlay:
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
              nixpkgs.overlays = [
                nur.overlays.default
                emacs-config.overlays.default
                overlay
              ];
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
  in
    flake-utils.lib.eachDefaultSystemPassThrough (system: rec {
      overlays = {
        default = let
          flakes = {
            inherit
              mujmap
              ;
          };

          stable = import nixpkgs-stable {
            inherit
              system
              ;
          };
        in
          import ./overlays {
            inherit flakes system stable;
          };
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
              system
              extraHomeManagerModules
              extraDarwinModules
              ;
          }
          darwinModules
          homeManagerModules
          overlays.default;
      in (builtins.mapAttrs op darwinSystems);

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
              system
              extraNixOSModules
              extraHomeManagerModules
              ;
          }
          nixosModules
          homeManagerModules
          overlays.default;
      in (builtins.mapAttrs op nixosSystems);

      inherit
        darwinModules
        homeManagerModules
        nixosModules
        ;
    })
    // flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };
    in {
      formatter = pkgs.alejandra;

      devShells = {
        default = pkgs.mkShell {
          buildInputs = with pkgs; [
            alejandra
            nil
          ];
        };
      };
    });
}
