{ flakes
, system }:

[
  flakes.nur.overlay

  (final: prev:

    {
      emacs = if final.stdenv.isDarwin
              then flakes.emacs-config.packages.${system}.wgn-emacs-macport
              else flakes.emacs-config.packages.${system}.wgn-emacs;

      mujmap = flakes.mujmap.packages.${system}.mujmap;

      exiv2 = prev.exiv2.overrideAttrs (oldAttrs:

        rec {
          version = "0.27-maintenance";

          src = final.fetchFromGitHub {
            owner = oldAttrs.src.owner;
            repo = oldAttrs.src.repo;
            rev = version;
            sha256 = "sha256-xytVGrLDS22n2/yydFTT6CsDESmhO9mFbPGX4yk+b6g=";
          };
        });

      librtprocess = prev.librtprocess.overrideAttrs (oldAttrs:

        rec {
          version = "9a858270acb2096e2e403d932760ee688fcac425";

          src = final.fetchFromGitHub {
            owner = "CarVac";
            repo = "librtprocess";
            rev = version;
            sha256 = "sha256-1NUiWoMiN0R0CmzS3okDzkOqTjz8gheeBw3gjm7W8ZE=";
          };
        });

      lensfun = prev.lensfun.overrideAttrs (oldAttrs:

        rec {
          version = "ab38daf3af23b32f3920f2d677e464d200c972ed";

          src = final.fetchFromGitHub {
            owner = "lensfun";
            repo = "lensfun";
            rev = version;
            sha256 = "sha256-QGngbovjaTmfuHquzKE2Z3pHDGBG8ZMTHN60fLdjRRA=";
          };

          patches = [ ];

          nativeBuildInputs = oldAttrs.nativeBuildInputs ++ [
            final.python3.pkgs.setuptools
          ];
        });

      filmulator-gui = let
        qtVersion = "5";
        qt = final."qt${qtVersion}";
        libsForQt = final."libsForQt${qtVersion}";
      in final.stdenv.mkDerivation rec {
        pname = "filmulator-gui";
        version = "68c055826ecead6f041e7f456b24b8596ec99b08";

        src = final.fetchFromGitHub {
          owner = "CarVac";
          repo = pname;
          rev = version;
          sha256 = "sha256-DlFOe7vWCV10dYRDmrYJc6Mdgszx1FdUp6ONxgbGX4s=";
        };

        sourceRoot = "source/filmulator-gui";
        cmakeFlags = [ "-DCMAKE_BUILD_TYPE=RELEASE" ];

        nativeBuildInputs = (with final; [
          cmake
          pkg-config
        ]) ++ (with qt; [
          wrapQtAppsHook
        ]);

        buildInputs = (with final; [
          curl
          exiv2
          lensfun
          libarchive
          libjpeg
          libraw
          librtprocess
          libtiff
        ]) ++ (with qt; [
          qtbase
          qtquickcontrols2
        ]) ++ (with libsForQt; [
          libkexiv2
        ]);

        meta.broken = builtins.compareVersions qt.qtbase.version "5.15" < 0;
      };

      # FIXME: This is blocked by CrowdStrike on my work laptop :(
      # https://github.com/Mozilla-Ocho/llamafile/issues/14
      llava = final.stdenv.mkDerivation rec {
        pname = "llava";
        version = "v1.5-7b-q4";

        src = final.fetchurl {
          url = "https://huggingface.co/jartine/${pname}-v1.5-7B-GGUF/resolve/main/${pname}-${version}-server.llamafile";
          hash = "sha256-O+6UYMSTsi/5wbPTP0rbwRY65Xuz6GtVZ/g7SyGE5lI=";
        };

        dontUnpack = true;

        installPhase = ''
          runHook preInstall

          mkdir -p $out/bin
          cp $src $out/bin/${pname}-${version}-server.llamafile
          chmod 755 $out/bin/${pname}-${version}-server.llamafile

          runHook postInstall
        '';
      };
    })
]
