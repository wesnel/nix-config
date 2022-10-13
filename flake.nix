{
  description = "configuration for nixOS / nix-darwin";

  inputs = {
    darwin = {
      url = github:lnl7/nix-darwin;
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-overlay = {
      url = github:nix-community/emacs-overlay;
      inputs.nixpkgs.follows = "nixpkgs";
    };

    home-manager = {
      url = github:nix-community/home-manager;
      inputs.nixpkgs.follows = "nixpkgs";
    };

    nixos-hardware = {
      url = github:nixos/nixos-hardware;
      inputs.nixpkgs.follows = "nixpkgs";
    };

    nixpkgs = {
      url = github:nixos/nixpkgs/nixos-unstable;
    };

    nur = {
      url = github:nix-community/NUR;
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = inputs@
  { darwin
  , emacs-overlay
  , home-manager
  , nixos-hardware
  , nixpkgs
  , nur
  , ...
  }: {
    darwinConfigurations = {
      wgn-shipt = let
        args = {
          system = "x86_64-darwin";
          username = "wesleynelson";
          homeDirectory = "/Users/${args.username}";

          brewPrefix = if nixpkgs.legacyPackages.${args.system}.stdenv.hostPlatform.isAarch64
            then "/opt/homebrew"
            else "/usr/local";
        };
      in darwin.lib.darwinSystem {
        system = args.system;

        modules = [
          # nix-darwin configuration:
          ./machines/shipt
          ./modules/darwin

          (inputs@{ ... }: {
            nixpkgs.overlays = [
              (import emacs-overlay)
              (import ./modules/home/programs/kitty/overlays)
            ];
          })

          home-manager.darwinModules.home-manager {
            imports = [
              # general home-manager configuration:
              ./modules/home-manager
            ];

            home-manager = {
              users = {
                # user-specific home-manager configuration:
                "${args.username}" = inputs@{ ... }: {
                  imports = [
                    # general functionality for all users:
                    ./modules/home
                    # config specifically for this user:
                    ./modules/home/users/wgn

                    # darwin-specific:
                    ./modules/home/programs/zsh
                    ./modules/home/skhd
                    ./modules/home/yabai
                  ];
                };
              };

              extraSpecialArgs = args;
            };
          }
        ];

        specialArgs = args;
      };
    };

    nixosConfigurations = {
      framework = let
        args = {
          system = "x86_64-linux";
          username = "wgn";
          homeDirectory = "/home/${args.username}";
          hostname = "framework";
        };
      in nixpkgs.lib.nixosSystem {
        system = args.system;

        modules = [
          # community configuration for the framework laptop:
          nixos-hardware.nixosModules.framework

          # nix user repository:
          nur.nixosModules.nur

          # nixOS configuration:
          ./machines/framework

          # use more recent emacs:
          (inputs@{ ... }: { nixpkgs.overlays = [ (import emacs-overlay) ]; })

          home-manager.nixosModules.home-manager {
            nixpkgs.overlays = [
              nur.overlay
            ];

            imports = [
              # general home-manager configuration:
              ./modules/home-manager
            ];

            home-manager = {
              users = {
                # user-specific home-manager configuration:
                "${args.username}" = inputs@{ ... }: {
                  imports = [
                    # general functionality for all users:
                    ./modules/home
                    # config specifically for this user:
                    ./modules/home/users/wgn

                    # nixOS-specific:
                    ./modules/home/programs/firefox
                    ./modules/home/services/gpg-agent
                    ./modules/home/services/lorri
                  ];
                };
              };

              extraSpecialArgs = args;
            };
          }
        ];

        specialArgs = args;
      };
    };
  };
}
