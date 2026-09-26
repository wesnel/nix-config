{
  lib,
  stdenvNoCC,
  fetchurl,
  unzip,
  autoPatchelfHook,
  zlib,
  # "host" is the binary that runs on this machine; "guest" is the Linux build
  # the Gondolin sandbox mounts. Both come from one table so that the sandboxed
  # and unsandboxed servers cannot drift to different versions.
  target ? "host",
}: let
  assets = {
    aarch64-darwin = {
      platform = "macos-aarch64";
      hash = "sha256-forsfElk1WoHcssgFbIkeadL477FhE6U6p/BGMGIH/Q=";
    };

    x86_64-darwin = {
      platform = "macos-amd64";
      hash = "sha256-1vyqnGcQZXOx2aYzAE/me/8yTOue0uvGVYB8QVKbkpg=";
    };

    aarch64-linux = {
      platform = "linux-aarch64";
      hash = "sha256-hmVq2UnuZH+sm4Dr9V6puAiEctHtMR0zZ9fbsRlLy/E=";
    };

    x86_64-linux = {
      platform = "linux-amd64";
      hash = "sha256-iDpadJcB3p+NB+ujk8puZtJU+QtSz8guIu0jFtcLQRY=";
    };
  };

  # Gondolin boots a guest of the host's own architecture, so anything else
  # would be emulated and is not worth selecting by accident.
  system =
    if target == "guest"
    then "${stdenvNoCC.hostPlatform.parsed.cpu.name}-linux"
    else stdenvNoCC.hostPlatform.system;

  asset =
    assets.${system}
    or (throw "eca: no release asset for ${system}");

  # Nothing here executes a guest binary, so it is only unpacked. Patching it
  # against this machine's loader would be wrong as well as unnecessary.
  isGuest = target == "guest";
  isForeign = isGuest && !stdenvNoCC.hostPlatform.isLinux;
in
  stdenvNoCC.mkDerivation (finalAttrs: {
    pname =
      if isGuest
      then "eca-guest"
      else "eca";

    version = "0.161.2";

    src = fetchurl {
      url = "https://github.com/editor-code-assistant/eca/releases/download/${finalAttrs.version}/eca-native-${asset.platform}.zip";

      inherit (asset) hash;
    };

    sourceRoot = ".";

    strictDeps = true;

    nativeBuildInputs =
      [unzip]
      ++ lib.optionals (stdenvNoCC.hostPlatform.isLinux && !isForeign) [
        autoPatchelfHook
      ];

    # The native image links zlib dynamically, which autoPatchelfHook has to
    # be able to resolve.
    buildInputs = lib.optionals (stdenvNoCC.hostPlatform.isLinux && !isForeign) [
      zlib
    ];

    # The upstream Mach-O binaries are signed with the hardened runtime, and
    # nothing here rewrites them, so the signature has to be left intact.
    dontFixup = stdenvNoCC.hostPlatform.isDarwin || isForeign;

    installPhase = ''
      runHook preInstall

      install -Dm755 eca $out/bin/eca

      runHook postInstall
    '';

    meta =
      {
        description = "Editor Code Assistant server";
        homepage = "https://github.com/editor-code-assistant/eca";
        license = lib.licenses.asl20;
      }
      // lib.optionalAttrs (!isGuest) {
        mainProgram = "eca";

        platforms = [
          "aarch64-darwin"
          "aarch64-linux"
          "x86_64-darwin"
          "x86_64-linux"
        ];
      };
  })
