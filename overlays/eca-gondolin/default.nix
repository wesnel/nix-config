{
  eca-guest,
  gondolin,
  makeWrapper,
  nodejs_24,
  stdenvNoCC,
}:
stdenvNoCC.mkDerivation {
  pname = "eca-gondolin";

  inherit (gondolin) version;

  dontUnpack = true;
  strictDeps = true;

  nativeBuildInputs = [makeWrapper];

  # The script resolves the SDK through ECA_GONDOLIN_LIB rather than an import
  # specifier, because Node ignores NODE_PATH for ESM and the package lives in
  # the store rather than a node_modules the script can reach.
  installPhase = ''
    runHook preInstall

    install -Dm755 ${./eca-gondolin.mjs} $out/libexec/eca-gondolin.mjs

    makeWrapper ${nodejs_24}/bin/node $out/bin/eca-gondolin \
      --add-flags $out/libexec/eca-gondolin.mjs \
      --set ECA_GONDOLIN_LIB ${gondolin}/lib/node_modules/@earendil-works/gondolin/dist/src/index.js \
      --set-default ECA_GONDOLIN_ECA ${eca-guest}/bin/eca \
      --run 'export ECA_GONDOLIN_CONFIG="''${ECA_GONDOLIN_CONFIG:-''${XDG_CONFIG_HOME:-$HOME/.config}/eca}"' \
      --run 'export ECA_GONDOLIN_STATE="''${ECA_GONDOLIN_STATE:-''${XDG_CACHE_HOME:-$HOME/.cache}/eca-gondolin}"' \
      --prefix PATH : ${gondolin}/bin

    # Both backends answer to one name so that a project's .dir-locals.el is
    # portable across machines; only one is ever installed.
    ln -s eca-gondolin $out/bin/eca-sandbox

    runHook postInstall
  '';

  meta = {
    description = "Runs the ECA server inside a Gondolin micro-VM over raw stdio";
    mainProgram = "eca-gondolin";
    inherit (gondolin.meta) platforms;
  };
}
