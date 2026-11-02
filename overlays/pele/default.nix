# Pele, the Mermaid renderer pstack's statechart skill renders charts with,
# as npm publishes it: it has no dependencies and ships built.
{
  fetchurl,
  lib,
  stdenvNoCC,
}:
stdenvNoCC.mkDerivation rec {
  pname = "pele";
  version = "1.9.0";

  src = fetchurl {
    url = "https://registry.npmjs.org/pele/-/pele-${version}.tgz";
    hash = "sha512-KUmBt7P5JduVKQaP6FKfXiRgH3LBVIKsmdgPJZU7jD3ye6F+Z8fbms+OjivE7rWY09Qx+5X65VKe/9ywKFJ9Sw==";
  };

  # Kept byte for byte as published: render.md sends agents to its files.
  dontFixup = true;

  installPhase = ''
    runHook preInstall

    mkdir -p $out/lib/node_modules/pele
    cp -R . $out/lib/node_modules/pele

    runHook postInstall
  '';

  meta = {
    description = "Mermaid renderer";
    homepage = "https://pele.run/";
    license = lib.licenses.mit;
  };
}
