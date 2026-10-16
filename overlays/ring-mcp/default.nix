{
  buildGoModule,
  git,
  lib,
}:
buildGoModule {
  pname = "ring-mcp";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.fileFilter (f: f.hasExt "go" || f.name == "go.mod") ./.;
  };

  vendorHash = null;
  env.CGO_ENABLED = "0";
  ldflags = ["-s" "-w"];

  # The capture test commits to a real repository.
  nativeCheckInputs = [git];

  meta = {
    description = "MCP server for a Pebble Index ring: capture to an org file in git, and read-only questions";
    mainProgram = "ring-mcp";
  };
}
