{
  bubblewrap,
  eca,
  lib,
  mitmproxy,
  python3,
  replaceVars,
  stdenvNoCC,
}:
stdenvNoCC.mkDerivation {
  pname = "eca-bwrap";

  inherit (eca) version;

  dontUnpack = true;
  strictDeps = true;

  installPhase = ''
    runHook preInstall

    install -Dm755 ${replaceVars ./eca-bwrap.sh {
      allowlist = ./allowlist.py;
      bwrap = lib.getExe bubblewrap;
      mitmdump = "${mitmproxy}/bin/mitmdump";
      python = lib.getExe python3;
    }} $out/bin/eca-bwrap

    # Both backends answer to one name so that a project's .dir-locals.el is
    # portable across machines; only one is ever installed.
    ln -s eca-bwrap $out/bin/eca-sandbox

    runHook postInstall
  '';

  meta = {
    description = "Runs the ECA server under bubblewrap with an intercepting proxy";
    mainProgram = "eca-bwrap";
    platforms = lib.platforms.linux;
  };
}
