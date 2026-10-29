{
  buildGoModule,
  git,
  gnupg,
  lib,
  nodejs,
  openssh,
  # wgn.home.llm.guard.credentialPaths, a path under home as ~/…, which the
  # guard falls back on where its config is absent.
  credentialPaths ? [],
  # The host's XDG config and state directories, which the programs then
  # take from the build, never from the environment of a command an agent
  # runs (internal/hostpaths). Without them, they read the environment.
  configHome ? "",
  stateHome ? "",
}: let
  # -ldflags is split at spaces.
  hostLdflags = lib.concatLists (lib.mapAttrsToList (
      var: dir:
        lib.optionals (dir != "") (
          assert lib.assertMsg (!lib.hasInfix " " dir) "agent-tools: ${var} ${dir} holds a space, which -ldflags cannot pass";
            ["-X" "github.com/wesnel/agent-tools/internal/hostpaths.${var}=${dir}"]
        )
    ) {
      builtConfigHome = configHome;
      builtStateHome = stateHome;
    });

  agent-tools = buildGoModule {
    pname = "agent-tools";
    version = "0.1.0";

    src = lib.fileset.toSource {
      root = ./.;
      fileset = lib.fileset.unions [./go.mod ./go.sum ./cmd ./internal];
    };

    # golang.org/x/mod, which reads go.mod and go.work as the go command
    # does. A change of go.mod's requirements changes this hash.
    vendorHash = "sha256-kIOJjGSNKkjPBnW9jPORKrxwVuTq2yLTzvbMjeiMx3U=";
    env.CGO_ENABLED = "0";
    ldflags =
      [
        "-s"
        "-w"
        "-X"
        "github.com/wesnel/agent-tools/internal/operatorkey.defaultGPGProgram=${lib.getExe gnupg}"
        "-X"
        "github.com/wesnel/agent-tools/internal/operatorkey.defaultSSHProgram=${lib.getExe' openssh "ssh-keygen"}"
      ]
      ++ hostLdflags;

    # Without a list, the guard counts everything under home as a credential
    # where its config is absent.
    preBuild = lib.optionalString (credentialPaths != []) ''
      cat > internal/guard/credentials_built.go <<'EOF'
      package guard

      func init() {
        builtCredentialPaths = []string{${lib.concatMapStringsSep ", " builtins.toJSON credentialPaths}}
      }
      EOF
    '';

    # The rehearsal and signing tests build stacks with the real git, and sign
    # them with throwaway SSH and OpenPGP keys. The page tests run the Agent plans
    # page's own grammar check with node; without it they skip that one.
    nativeCheckInputs = [git gnupg nodejs openssh];

    # The MCP tools the guard lets through (reads) or asks the operator
    # about (confirms), per service, for mcp-proxy's tool filters.
    passthru.mcpTools = builtins.fromJSON (builtins.readFile ./internal/guard/mcp.json);

    # Each focused review's contract, by route, as `git delegate --reviewer
    # ROUTE` builds it (focus.contract in internal/review): the general
    # contract up to its Checks heading, then internal/review/ROUTE.md.
    # TestFocusContracts holds the Go side to the same heading. The routes
    # are routes.Known's, from the same known.json, and the Claude Code
    # module gives each its ROUTE-reviewer agent.
    passthru.focusPrompts = let
      parts = lib.splitString "\n## Checks\n" (builtins.readFile ./internal/review/reviewer.md);
    in
      assert lib.assertMsg (builtins.length parts == 2) "agent-tools: reviewer.md needs exactly one `## Checks` heading, where the focused contracts split it";
        lib.genAttrs (builtins.fromJSON (builtins.readFile ./internal/routes/known.json))
        (route: builtins.head parts + "\n" + builtins.readFile (./internal/review + "/${route}.md"));

    meta.description = "pr-loop (the review loop), git-sign-pass (the signing pass), git-red-check (checks a commit's tests fail without its change), git-delegate (reviews a range or a pull request with reviewers that did not write it, and keeps their findings), git-push-guard (keeps private history out of public repositories), agent-plan (agents' plan files and event logs) and plan-dash (the Agent plans page's documents)";
  };
in
  agent-tools
