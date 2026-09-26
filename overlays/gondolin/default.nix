{
  lib,
  buildNpmPackage,
  fetchurl,
  jq,
  makeWrapper,
  nodejs_24,
  qemu,
}: let
  version = "0.12.0";
in
  buildNpmPackage {
    pname = "gondolin";

    inherit version;

    # Gondolin requires the ESM/type-stripping support in newer Node.
    nodejs = nodejs_24;

    # The GitHub repository is a pnpm workspace, but the npm tarball ships the
    # already-compiled host package, which is all the CLI and SDK need.
    src = fetchurl {
      url = "https://registry.npmjs.org/@earendil-works/gondolin/-/gondolin-${version}.tgz";
      hash = "sha256-J61m91/naoSDiCkfohjprJrG2sR7moGKP8xYkBaj+eI=";
    };

    # The optional dependencies are ~34MB of prebuilt binaries for the
    # experimental krun backend. Dropping them keeps us on the default QEMU
    # backend, which is the supported one.
    #
    # The tarball has no lockfile, so package-lock.json beside this file is
    # generated from the pruned package.json and pins the dependency tree.
    postPatch = ''
      ${lib.getExe jq} 'del(.optionalDependencies)' package.json > package.json.pruned
      mv package.json.pruned package.json
      cp ${./package-lock.json} package-lock.json
    '';

    npmDepsHash = "sha256-LVPLYoihHF+2TC/A3025EhfrijHWQMuzK+V2PKLOeEg=";

    # dist/ is prebuilt in the tarball, and ssh2's optional native accelerator
    # is not worth a node-gyp toolchain: it degrades gracefully when absent.
    dontNpmBuild = true;
    npmFlags = ["--ignore-scripts"];

    nativeBuildInputs = [makeWrapper];

    postInstall = ''
      wrapProgram $out/bin/gondolin \
        --prefix PATH : ${lib.makeBinPath [qemu]}
    '';

    meta = {
      description = "Local Linux micro-VMs with programmable network and filesystem control";
      homepage = "https://github.com/earendil-works/gondolin";
      license = lib.licenses.asl20;
      mainProgram = "gondolin";
      platforms = lib.platforms.unix;
    };
  }
