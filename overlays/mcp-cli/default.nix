{
  bun,
  fetchFromGitHub,
  stdenv,
  stdenvNoCC,
}: let
  pname = "mcp-cli";
  version = "0.3.0";

  src = fetchFromGitHub {
    owner = "philschmid";
    repo = "mcp-cli";
    rev = "v${version}";
    hash = "sha256-S924rqlVKzUFD63NDyK5bbXnonra+/UoH6j78AAj3d0=";
  };

  # Prefetch the bun dependency tree into its own derivation whose
  # output is pinned by `outputHash`, so Nix grants the build
  # network access.  A plain derivation has no network in a
  # sandboxed build (the default on Linux), which is why
  # `bun install` inside the main `buildPhase` used to fail there
  # while silently succeeding on Darwin, where Nix leaves the
  # sandbox off by default.  With the dependencies staged into the
  # Nix store this way, the main build below can run offline.
  #
  # Only production dependencies are fetched: `bun build --compile`
  # bundles from `node_modules`, and the Biome devDependency drags
  # in platform-specific binaries that would give this output a
  # different hash on each system.
  node-modules = stdenvNoCC.mkDerivation {
    pname = "${pname}-node-modules";

    dontConfigure = true;
    dontFixup = true;

    nativeBuildInputs = [bun];

    buildPhase = ''
      runHook preBuild

      export HOME=$TMPDIR
      bun install \
        --frozen-lockfile \
        --production \
        --ignore-scripts \
        --no-progress

      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall

      mkdir -p $out
      cp -R node_modules $out/

      runHook postInstall
    '';

    outputHashMode = "recursive";
    outputHashAlgo = "sha256";
    outputHash = "sha256-yDqCHnjmkz8dotufExIb5qRE04fLDWlEy+1u1YzFBqs=";

    inherit
      src
      version
      ;
  };
in
  stdenv.mkDerivation {
    nativeBuildInputs = [bun];

    configurePhase = ''
      runHook preConfigure

      cp -R ${node-modules}/node_modules ./node_modules
      chmod -R u+w node_modules

      runHook postConfigure
    '';

    buildPhase = ''
      runHook preBuild

      bun build \
        --compile \
        --minify \
        src/index.ts \
        --outfile dist/mcp-cli

      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall

      install -Dm755 dist/mcp-cli $out/bin/mcp-cli

      runHook postInstall
    '';

    meta = {
      description = "Interface for MCP servers via CLI";
      homepage = "https://github.com/philschmid/mcp-cli";
      mainProgram = "mcp-cli";
    };

    inherit
      pname
      src
      version
      ;
  }
