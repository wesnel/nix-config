{
  buildGoModule,
  git,
  lib,
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
      fileset = lib.fileset.unions [./go.mod ./cmd ./internal];
    };

    vendorHash = null;
    env.CGO_ENABLED = "0";
    ldflags = ["-s" "-w"] ++ hostLdflags;

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
    # them with a throwaway SSH key.
    nativeCheckInputs = [git openssh];

    # The MCP tools the guard lets through (reads) or asks the operator
    # about (confirms), per service, for mcp-proxy's tool filters.
    passthru.mcpTools = builtins.fromJSON (builtins.readFile ./internal/guard/mcp.json);

    # The security review's contract as `git fresh-review --focus security`
    # builds it (focus.contract in internal/review): the general contract up
    # to its Checks heading, then the security checks. TestSecurityContract
    # holds the Go side to the same heading.
    passthru.securityReviewerPrompt = let
      parts = lib.splitString "\n## Checks\n" (builtins.readFile ./internal/review/reviewer.md);
    in
      assert lib.assertMsg (builtins.length parts == 2) "agent-tools: reviewer.md needs exactly one `## Checks` heading, where the security contract splits it";
        builtins.head parts + "\n" + builtins.readFile ./internal/review/security.md;

    meta.description = "pr-loop (the review loop), git-sign-pass (the signing pass), git-red-check (checks a commit's tests fail without its change), git-fresh-review (reviews a range of commits with fresh eyes), git-push-guard (keeps private history out of public repositories), agent-plan (agents' plan files and event logs) and plan-dash (the Agent plans page's documents)";
  };
in
  agent-tools
