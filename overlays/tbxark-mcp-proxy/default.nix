{
  buildGoModule,
  fetchFromGitHub,
  lib,
}:
buildGoModule rec {
  pname = "mcp-proxy";
  version = "1.1.0";

  src = fetchFromGitHub {
    owner = "TBXark";
    repo = "mcp-proxy";
    tag = "v${version}";
    hash = "sha256-g3up2T63z6sbswtXryfgmO7j3QrjfRICqJmFayQoi7g=";
  };

  # TBXark/mcp-proxy#83: the caller's per-connection _meta must not reach
  # downstream servers. Carries its own e2e test, run in checkPhase.
  patches = [./strip-connection-meta.patch];

  vendorHash = "sha256-yLJrc9gbnzNEdjYg8XhXwRGKmc+p7xTjX1eZfWMKc2Q=";
  env.CGO_ENABLED = "0";
  ldflags = ["-s" "-w" "-X main.BuildVersion=${version}"];

  meta = {
    description = "MCP proxy that aggregates servers behind one HTTP listener, with tool filters and OAuth to remote servers";
    homepage = "https://github.com/TBXark/mcp-proxy";
    license = lib.licenses.mit;
    # nixpkgs' mcp-proxy is a different project with the same binary name.
    mainProgram = "mcp-proxy";
  };
}
