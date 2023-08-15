{ flakes
, system }:

[
  (final: prev:

    {
      emacs = if final.stdenv.isDarwin
              then flakes.emacs-config.legacyPackages.${system}.wgn-emacs-macport
              else flakes.emacs-config.legacyPackages.${system}.wgn-emacs;

      mujmap = flakes.mujmap.packages.${system}.mujmap;

      filmulator-gui = let
        qtVersion = "5";
        qt = final."qt${qtVersion}";
        libsForQt = final."libsForQt${qtVersion}";

        librtprocess = final.librtprocess.overrideAttrs (oldAttrs:

            rec {
              version = "0.12.0";

              src = final.fetchFromGitHub {
                owner = "CarVac";
                repo = "librtprocess";
                rev = version;
                sha256 = "sha256-/1o6SWUor+ZBQ6RsK2PoDRu03jcVRG58PNYFttriH2w=";
              };
            });

        lensfun = final.lensfun.overrideAttrs (oldAttrs:

          rec {
            version = "c3f21f42e6b6fd502fedb6c96fc2e1d494d52f2e";

            src = final.fetchFromGitHub {
              owner = "lensfun";
              repo = "lensfun";
              rev = version;
              sha256 = "sha256-sE/b1aXl+/PesEgu56Z6iDKSmdH66aXZ2nau7BhafLw=";
            };

            patches = [ ];

            nativeBuildInputs = oldAttrs.nativeBuildInputs ++ [
              final.python3.pkgs.setuptools
            ];
          });
      in final.stdenv.mkDerivation rec {
        pname = "filmulator-gui";
        version = "0.11.0";

        src = final.fetchFromGitHub {
          owner = "CarVac";
          repo = pname;
          rev = "v${version}";
          sha256 = "0gdw4mxawh8lh4yvq521h3skjc46fpb0wvycy4sl2cm560d26sy6";
        };

        sourceRoot = "source/filmulator-gui";
        cmakeFlags = [ "-DCMAKE_BUILD_TYPE=RelWithDebInfo" ];

        nativeBuildInputs = (with final; [
          cmake
          pkg-config
        ]) ++ (with qt; [
          wrapQtAppsHook
        ]);

        buildInputs = ([
          lensfun
          librtprocess
        ]) ++ (with final; [
          curl
          exiv2
          libarchive
          libjpeg
          libraw
          libtiff
        ]) ++ (with qt; [
          qtbase
          qtquickcontrols2
        ]) ++ (with libsForQt; [
          libkexiv2
        ]);

        meta.broken = builtins.compareVersions qt.qtbase.version "5.15" < 0;
      };
    })
]
