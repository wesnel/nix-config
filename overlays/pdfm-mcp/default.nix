{
  stdenvNoCC,
  fetchurl,
  lib,
}:
stdenvNoCC.mkDerivation rec {
  pname = "pdfm-mcp";
  version = "0.1.4";

  src = fetchurl {
    url = "https://github.com/Parallels/pdfm-mcp/releases/download/v${version}/pdfm-mcp_${version}_darwin_universal.tar.gz";
    hash = "sha256-l1qOxVo1Pcieh6xqjVgbkQhW7l3O9CdBOV05zt5vHn8=";
  };

  sourceRoot = ".";
  dontBuild = true;
  # Preserve the upstream universal executable and its code signature.
  dontFixup = true;

  installPhase = ''
    runHook preInstall
    install -Dm755 pdfm-mcp "$out/bin/pdfm-mcp"
    install -Dm644 LICENSE "$out/share/licenses/pdfm-mcp/LICENSE"
    runHook postInstall
  '';

  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck
    "$out/bin/pdfm-mcp" --version | grep -F 'pdfm-mcp ${version} '
    runHook postInstallCheck
  '';

  meta = {
    description = "MCP server for Parallels Desktop virtual machines";
    homepage = "https://github.com/Parallels/pdfm-mcp";
    license = lib.licenses.mit;
    platforms = lib.platforms.darwin;
    mainProgram = "pdfm-mcp";
    sourceProvenance = [lib.sourceTypes.binaryNativeCode];
  };
}
