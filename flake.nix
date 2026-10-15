{
  description = "Wesley's Nix Configurations";

  inputs = {
    nix-darwin = {
      url = "github:lnl7/nix-darwin/master";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    deploy-rs = {
      url = "github:serokell/deploy-rs";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-config = {
      url = "github:wesnel/emacs-config";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-skills = {
      url = "github:xenodium/emacs-skills";
      flake = false;
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

    mac-app-util = {
      url = "github:hraban/mac-app-util";
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

    # x86_64-darwin machines build from these instead: nixpkgs 26.05 is
    # the last release that supports the platform, and 26.11 refuses to
    # evaluate for it. Keep all three on matching 26.05 release branches.
    nixpkgs-x86_64-darwin = {
      url = "github:nixos/nixpkgs/nixpkgs-26.05-darwin";
    };

    nix-darwin-x86_64-darwin = {
      url = "github:lnl7/nix-darwin/nix-darwin-26.05";
      inputs.nixpkgs.follows = "nixpkgs-x86_64-darwin";
    };

    home-manager-x86_64-darwin = {
      url = "github:nix-community/home-manager/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs-x86_64-darwin";
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
    deploy-rs,
    emacs-config,
    emacs-skills,
    flake-utils,
    home-manager,
    mac-app-util,
    nixos-hardware,
    nixpkgs,
    nixpkgs-x86_64-darwin,
    nix-darwin-x86_64-darwin,
    home-manager-x86_64-darwin,
    nur,
    sops-nix,
  }: let
    key = "0xA776D2AD099E8BC0";

    sops = {homeDirectory, ...}: {
      sops = {
        defaultSopsFile = ./secrets/wgn.yaml;
        defaultSopsFormat = "yaml";

        age = {
          # This is using an age key that is expected to already be in the filesystem
          keyFile = "${homeDirectory}/.config/sops-nix/key.txt";
          # This will generate a new key if the key specified above does not exist
          generateKey = true;
        };
      };
    };

    homeManagerModules = [
      sops-nix.homeManagerModules.sops
      sops

      mac-app-util.homeManagerModules.default

      (_: {
        _module.args = {
          inherit
            emacs-skills
            ;
        };
      })

      ./modules/home-manager/aerospace
      ./modules/home-manager/amp
      ./modules/home-manager/bash
      ./modules/home-manager/claude
      ./modules/home-manager/codex
      ./modules/home-manager/copilot
      ./modules/home-manager/docs-mcp-server
      ./modules/home-manager/eca
      ./modules/home-manager/emacs
      ./modules/home-manager/firefox
      ./modules/home-manager/fish
      ./modules/home-manager/fonts
      ./modules/home-manager/foot
      ./modules/home-manager/gamedev
      ./modules/home-manager/games
      ./modules/home-manager/gcloud
      ./modules/home-manager/ghostty
      ./modules/home-manager/git
      ./modules/home-manager/gnupg
      ./modules/home-manager/go
      ./modules/home-manager/helix
      ./modules/home-manager/hyprland
      ./modules/home-manager/iterm
      ./modules/home-manager/llm
      ./modules/home-manager/man
      ./modules/home-manager/mcp-proxy
      ./modules/home-manager/mosh
      ./modules/home-manager/music
      ./modules/home-manager/ollama
      ./modules/home-manager/photos
      ./modules/home-manager/python
      ./modules/home-manager/slack
      ./modules/home-manager/sway
      ./modules/home-manager/tex
      ./modules/home-manager/video
      ./modules/home-manager/virtualisation
      ./modules/home-manager/yubikey
      ./modules/home-manager/zellij
      ./modules/home-manager/zoom
      ./modules/home-manager/zwift
    ];

    nixosModules = [
      sops-nix.nixosModules.sops
      sops

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
      ./modules/nixos/zsa
      ./modules/nixos/zwift
    ];

    darwinModules = [
      mac-app-util.darwinModules.default

      ./modules/darwin/defaults
      ./modules/darwin/emacs
      ./modules/darwin/fish
      ./modules/darwin/fonts
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

            (_: {
              wgn.nixos = {
                zsa.enable = true;
              };
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
    }: darwinModules: homeManagerModules: overlay: let
      isX86 = system == "x86_64-darwin";
      darwin =
        if isX86
        then nix-darwin-x86_64-darwin
        else nix-darwin;
      hm =
        if isX86
        then home-manager-x86_64-darwin
        else home-manager;
    in
      darwin.lib.darwinSystem {
        inherit
          system
          ;

        modules =
          darwinModules
          ++ extraDarwinModules
          ++ [
            (_: {
              system.configurationRevision = self.rev or self.dirtyRev or null;
              system.stateVersion = 6;
            })

            (_: {
              nixpkgs.overlays = [
                nur.overlays.default
                emacs-config.overlays.default
                overlay
              ];
            })

            (_: {
              nixpkgs.config.allowDeprecatedx86_64Darwin = isX86;
            })

            # mac-app-util is written in Common Lisp, and nixpkgs marks
            # sbcl broken on x86_64-darwin.
            ({lib, ...}:
              lib.mkIf isX86 {
                services.mac-app-util.enable = false;

                home-manager.sharedModules = [
                  {
                    targets.darwin.mac-app-util.enable = false;
                  }
                ];
              })
          ]
          ++ [
            hm.darwinModules.home-manager
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

    buildHomeManagerConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      system,
      extraHomeManagerModules,
    }: homeManagerModules: overlay:
      home-manager.lib.homeManagerConfiguration {
        pkgs = import nixpkgs {
          inherit system;

          overlays = [
            nur.overlays.default
            emacs-config.overlays.default
            overlay
          ];

          config = {
            allowUnfree = true;
          };
        };

        modules =
          homeManagerModules
          ++ extraHomeManagerModules
          ++ [
            ({lib, ...}: {
              home = {
                inherit
                  username
                  homeDirectory
                  ;

                stateVersion = lib.mkDefault "22.05";
              };

              programs.home-manager.enable = true;
            })
          ];

        extraSpecialArgs = args;
      };

    homeManagerSystems = import ./machines/home-manager {
      inherit
        emacs-config
        ;
    };

    # deploy-rs.lib for a given system, with the deploy-rs binary that the
    # activation wrappers embed taken from nixpkgs rather than built from the
    # flake's own source.
    deployLib = system: let
      pkgs = import nixpkgs {
        inherit system;
      };
    in
      (import nixpkgs {
        inherit system;

        overlays = [
          deploy-rs.overlays.default

          (_: prev: {
            deploy-rs = {
              inherit (prev.deploy-rs) lib;
              inherit (pkgs) deploy-rs;
            };
          })
        ];
      })
      .deploy-rs
      .lib;

    buildDeployNode = {
      computerName,
      username,
      homeDirectory,
      system,
      deploy,
      extraHomeManagerModules,
    }: homeConfiguration:
      deploy
      // {
        profiles = {
          home = {
            user = username;
            sshUser = username;

            # home-manager points ../profiles/home-manager at its own
            # generation on every activation, so deploy-rs needs a profile of
            # its own to roll back.
            profilePath = "${homeDirectory}/.local/state/nix/profiles/home";

            path = (deployLib system).activate.home-manager homeConfiguration;
          };
        };
      };
  in
    flake-utils.lib.eachDefaultSystemPassThrough (system: rec {
      overlays = {
        default = import ./overlays;
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

      deploy = {
        nodes = let
          op = name: {
            computerName,
            username,
            homeDirectory,
            system,
            deploy,
            extraHomeManagerModules,
          }:
            buildDeployNode
            {
              inherit
                computerName
                username
                homeDirectory
                system
                deploy
                extraHomeManagerModules
                ;
            }
            homeConfigurations.${name};
        in (builtins.mapAttrs op homeManagerSystems);
      };

      homeConfigurations = let
        op = _: {
          computerName,
          username,
          homeDirectory,
          system,
          deploy,
          extraHomeManagerModules,
        }:
          buildHomeManagerConfiguration
          {
            inherit
              computerName
              username
              homeDirectory
              key
              system
              extraHomeManagerModules
              ;
          }
          homeManagerModules
          overlays.default;
      in (builtins.mapAttrs op homeManagerSystems);
    })
    // flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };
    in {
      formatter = pkgs.alejandra;

      devShells = {
        default = pkgs.mkShell {
          buildInputs = [
            # Not `with pkgs`: the deploy-rs flake input shadows pkgs.deploy-rs.
            pkgs.alejandra
            pkgs.deploy-rs
            pkgs.nil
          ];
        };
      };
    });
}
