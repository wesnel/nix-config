{ flakes
, system }:

[
  flakes.nur.overlay

  (final: prev:

    {
      emacs = if final.stdenv.isDarwin
              then flakes.emacs-config.packages.${system}.wgn-emacs-macport
              else flakes.emacs-config.packages.${system}.wgn-emacs-unstable;

      mujmap = flakes.mujmap.packages.${system}.mujmap;

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
