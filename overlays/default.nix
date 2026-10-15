final: prev: {
  agent-tools = final.callPackage ./agent-tools {};

  tbxark-mcp-proxy = final.callPackage ./tbxark-mcp-proxy {};

  zwift-offline-image = let
    imageDigest = "sha256:74d461d0271c1b11520a3d8aa8973dc9254fcecd2ef07c7ed53c618a741279b0";
    sha256 = "0q97352z34mjmximiqhirn2pzrljkiiw07cshp9yvzr69kj2aqa3";
  in
    final.dockerTools.pullImage {
      imageName = "zoffline/zoffline";
      finalImageName = "zoffline";
      finalImageTag = "zoffline_1.0.135674";

      inherit
        imageDigest
        sha256
        ;
    };

  zwift-offline-cert = let
    rev = "dffcb8e486af1812c9ca42b823301d4ed7e1afbb";
    hash = "sha256-lSayeT2ZJUdq71mnbrZ3Iw1rWwvmVR1oLrS3rll5HBQ=";
  in
    final.stdenv.mkDerivation {
      name = "zwift-offline-cert";

      src = final.fetchgit {
        url = "https://github.com/zoffline/zwift-offline.git";

        sparseCheckout = [
          "ssl"
        ];

        inherit
          rev
          hash
          ;
      };

      buildPhase = ''
        runHook preBuild

        mkdir -p $out
        cp $src/ssl/cert-zwift-com.pem $out

        runHook postBuild
      '';
    };

  zwift-image = final.dockerTools.buildLayeredImage {
    name = "zwift-image";
    tag = "1.74.2";

    fromImage = let
      imageDigest = "sha256:27259b3f79e0270b36bef093dcf22189c5e2e86bf05e5de4332bf1f561ca3b8f";
      sha256 = "sha256-Jlzrl8LhU0R2J00JmTWVCtvM42oQSra5ImGNCw+R2yU=";
    in
      final.dockerTools.pullImage {
        imageName = "netbrain/zwift";
        finalImageName = "zwift";
        finalImageTag = "1.74.2";

        inherit
          imageDigest
          sha256
          ;
      };

    config = {
      Entrypoint = ["entrypoint"];
      Cmd = ["$@"];
    };
  };

  zwift-script = let
    rev = "ac8eb25cebc2714e8df779d001a05ec8b725ddad";
    hash = "sha256-oNo2dOw3R/pd3IhVbT2AbG709SUIHaqbkTTJ57vO56c=";
  in
    final.stdenv.mkDerivation {
      name = "zwift-script";

      src = final.fetchurl {
        url = "https://raw.githubusercontent.com/netbrain/zwift/${rev}/zwift.sh";
        inherit hash;
      };

      buildInputs = with final; [
        bash
      ];

      unpackPhase = ''
        runHook preUnpack

        mkdir -p $out/bin
        cp $src $out/bin/zwift.sh

        runHook postUnpack
      '';

      installPhase = ''
        runHook preInstall

        chmod +x $out/bin/zwift.sh

        runHook postInstall
      '';
    };
}
