{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.codex;

  llm = import ../llm/lib.nix {inherit lib;};

  mcpProxy = config.wgn.home.mcp-proxy;

  registerMcpServers = pkgs.writers.writePython3 "codex-mcp-servers" {libraries = [pkgs.python3Packages.tomlkit];} ''
    import json
    import os
    import sys

    import tomlkit

    path = sys.argv[1]
    servers = json.loads(sys.argv[2])

    doc = tomlkit.document()
    if os.path.exists(path):
        with open(path) as f:
            doc = tomlkit.parse(f.read())

    tables = doc.setdefault("mcp_servers", tomlkit.table(is_super_table=True))
    changed = False
    for name, entry in servers.items():
        if tables.get(name) != entry:
            table = tomlkit.table()
            table.update(entry)
            tables[name] = table
            changed = True

    if changed:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(tomlkit.dumps(doc))
  '';

  # Sets keys of config.toml's [notice] table, keeping the rest of the file.
  setNotices = pkgs.writers.writePython3 "codex-notices" {libraries = [pkgs.python3Packages.tomlkit];} ''
    import json
    import os
    import sys

    import tomlkit

    path = sys.argv[1]
    notices = json.loads(sys.argv[2])

    doc = tomlkit.document()
    if os.path.exists(path):
        with open(path) as f:
            doc = tomlkit.parse(f.read())

    table = doc.setdefault("notice", tomlkit.table())
    changed = False
    for key, value in notices.items():
        if table.get(key) != value:
            table[key] = value
            changed = True

    if changed:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(tomlkit.dumps(doc))
  '';

  # Sets config.toml's top-level keys and its tables' keys from a JSON
  # object, keeping the rest of the file.
  setKeys = pkgs.writers.writePython3 "codex-set-keys" {libraries = [pkgs.python3Packages.tomlkit];} ''
    import json
    import os
    import sys

    import tomlkit

    path = sys.argv[1]
    keys = json.loads(sys.argv[2])

    doc = tomlkit.document()
    if os.path.exists(path):
        with open(path) as f:
            doc = tomlkit.parse(f.read())

    changed = False
    for key, value in keys.items():
        if isinstance(value, dict):
            table = doc.setdefault(key, tomlkit.table())
            for k, v in value.items():
                if table.get(k) != v:
                    table[k] = v
                    changed = True
        elif doc.get(key) != value:
            doc[key] = value
            changed = True

    if changed:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(tomlkit.dumps(doc))
  '';

  # The role agents Claude Code starts (llm.agentFiles), as Codex roles:
  # ~/.codex/agents/NAME.toml, which spawn_agent's agent_type names. Codex
  # takes a role's prompt and effort but no tools or hooks of its own, so a
  # reviewer's role does not keep it to reading. Codex preloads no skills,
  # so the prompt names those its agent file lists.
  roles = mapAttrs' (name: a:
    nameValuePair ".codex/agents/${name}.toml" {
      source = (pkgs.formats.toml {}).generate "codex-role-${name}.toml" ({
          inherit name;
          description = a.field "description";
          developer_instructions =
            optionalString (a.skills != []) "Load these skills before anything else: ${concatStringsSep ", " a.skills}.\n\n"
            + a.prompt;
        }
        // optionalAttrs (a.field "effort" != null) {model_reasoning_effort = a.field "effort";});
    }) (llm.agentFiles pkgs (getExe' config.wgn.home.llm.agentTools "pr-loop"));

  plans = config.wgn.home.llm.plans;
  sandboxLists = config.wgn.home.llm.sandbox;
  darwin = pkgs.stdenv.hostPlatform.isDarwin;

  # The shared denies Codex leaves out, which the assertion names so that
  # no other goes missing.
  omittedDenies =
    # They hold the git.inc template, which git must read; deniedPaths
    # denies everything else under them instead.
    optional (sandboxLists.allowedReads != []) (
      if darwin
      then "/private/var/folders/*/*/T/secrets.d"
      else "/run/user"
    )
    # Under ~/.ssh, which is denied whole.
    ++ [plans.repository.key];

  # The shared deny-read list as Codex takes it. Codex emits its deny rules
  # after every grant, so no read can be carved out of one: a path holding
  # one of sandboxLists.allowedReads, since git dies on an include it
  # cannot read, is replaced by denies of everything under it but the path
  # to that read, level by level. A path under another is left out, which
  # Linux's sandbox cannot mount inside the one it hides. A deny holding a
  # glob matches only the path itself in Codex, so `/**` is added for what
  # lies under it.
  deniedPaths = let
    under = p: d: p == d || hasPrefix "${d}/" p;
    holdsAllowedRead = p: any (r: under r p) sandboxLists.allowedReads;
    nested = p: any (d: d != p && under p d) sandboxLists.deniedReads;
    hasGlob = p: match ".*[][*?{}].*" p != null;
    # Globs for every name of one path component but `name`: shorter,
    # longer, and of its length differing in one character. Codex's
    # `[!…]` matches `/` unless the class names it.
    otherNames = name: let
      n = stringLength name;
      chars = stringToCharacters name;
      anyOf = k: concatStrings (genList (_: "?") k);
    in
      genList (k: anyOf (k + 1)) (n - 1)
      ++ [(anyOf (n + 1) + "*")]
      ++ genList (i: anyOf i + "[!/${elemAt chars i}]" + anyOf (n - 1 - i)) n;
    # A `*` level of an allowed read is a user ID or a sops-nix
    # generation, so every name there that is not a number is denied.
    carve = prefix: components:
      optionals (components != []) (
        (
          if head components == "*"
          then ["${prefix}/[!/0-9]*" "${prefix}/[0-9]*[!/0-9]*"]
          else map (o: "${prefix}/${o}") (otherNames (head components))
        )
        ++ carve "${prefix}/${head components}" (tail components)
      );
    carved = d:
      concatMap (r: carve d (splitString "/" (removePrefix "${d}/" r)))
      (filter (r: hasPrefix "${d}/" r) sandboxLists.allowedReads);
  in
    concatMap (p: [p] ++ optional (hasGlob p) "${p}/**") (
      filter (p: !holdsAllowedRead p && !nested p) sandboxLists.deniedReads
      ++ concatMap carved (filter holdsAllowedRead sandboxLists.deniedReads)
    );

  # Whether Codex's deny `pattern` covers `path`, as Codex 0.165 turns a
  # glob into a Seatbelt regex (codex-rs/sandboxing/src/seatbelt.rs,
  # seatbelt_regex_for_glob), for the syntax used here: `*`, `**`, `?` and
  # `[!…]`. A pattern without a glob covers what lies under it too.
  codexDenies = pattern: path: let
    special = ["." "+" "(" ")" "{" "}" "|" "^" "$" "\\"];
    go = cs:
      if cs == []
      then ""
      else let
        c = head cs;
        rest = tail cs;
      in
        if c == "*" && rest != [] && head rest == "*"
        then
          if tail rest != [] && head (tail rest) == "/"
          then "(.*/)?" + go (tail (tail rest))
          else ".*" + go (tail rest)
        else if c == "*"
        then "[^/]*" + go rest
        else if c == "?"
        then "[^/]" + go rest
        else if c == "["
        then let
          end = lists.findFirstIndex (x: x == "]") 0 rest;
          class = take end rest;
        in
          "["
          + concatStrings (
            if class != [] && head class == "!"
            then ["^"] ++ tail class
            else class
          )
          + "]"
          + go (drop (end + 1) rest)
        else optionalString (elem c special) "\\" + c + go rest;
    expanded =
      if hasPrefix "~/" pattern
      then "${config.home.homeDirectory}/${removePrefix "~/" pattern}"
      else pattern;
    glob = match ".*[][*?].*" expanded != null;
  in
    match (go (stringToCharacters expanded) + optionalString (!glob) "(/.*)?") path != null;

  # The excluded commands an approved run can complete in Codex, as Codex
  # rule prefixes: "agent-plan new *" is ["agent-plan" "new"]. gh-read,
  # pr-loop and git delegate need the gh login or the reviewers'
  # credentials, which no command in the profile can read, approved or
  # not, so asking before them would only ask for a failure; pr-loop's
  # and git delegate's run as pr-loop mcp's tools instead.
  excludedPrefixes = map (c: splitString " " (removeSuffix " *" c)) (
    filter (c: !(any (p: hasPrefix p c) ["gh-read " "git delegate " "git-delegate " "pr-loop "]))
    config.wgn.home.llm.guard.excludedCommands
  );

  # Commands' temporary directory on macOS, in place of the per-user one,
  # which holds the decrypted secrets and is read-only to them.
  commandTmpdir = "${config.xdg.cacheHome}/codex/tmp";
in {
  options.wgn.home.codex = {
    enable = mkEnableOption "Enables my Codex skills for home-manager";

    quotaStop = mkOption {
      type = types.nullOr (types.ints.between 1 100);
      # Through a gateway Codex spends the gateway's quota, not the OpenAI
      # account's windows that codex-usage reads.
      default =
        if config.wgn.home.llm.gateway == "none"
        then 90
        else null;
      defaultText = literalExpression ''if config.wgn.home.llm.gateway == "none" then 90 else null'';

      description = ''
        The share of a Codex account quota window, in percent, at which
        every Codex session stops. `agent-plan codex-usage` records the
        account's windows every minute from a launchd agent (a systemd
        timer on Linux), outside every session. While a window recorded in
        the last five minutes, and not yet reset, is at or above this share,
        the PreToolUse hook `agent-plan quota-stop --harness codex` refuses
        every tool call but `update_plan`, `request_user_input`,
        `wait_agent`, `close_agent` and `agent-plan` alone on its line, and
        tells the session to end its turn for the operator to resume after
        the reset. Codex trusts the hook only once it is approved with
        /hooks, again after this value changes. Nothing in a session lifts
        the stop; null turns it off at the next switch. Must be null
        through a gateway ({option}`wgn.home.llm.gateway`).
      '';
    };

    sandbox = mkOption {
      type = types.attrs;
      internal = true;
      default = {
        default_permissions = "wgn";
        permissions.wgn = {
          extends = ":workspace";
          filesystem =
            genAttrs deniedPaths (_: "deny")
            # The plans directory is not among them: only agent-plan's
            # writes, run bare and approved, write the plans, as in Claude
            # Code's sandbox. Codex's sandbox cannot tell agent-plan from a
            # command wrapping it.
            // genAttrs sandboxLists.toolCaches (_: "write")
            # On macOS sops-nix decrypts under the per-user temporary
            # directory, where a command could otherwise rewrite the git.inc
            # template git includes; commands write commandTmpdir instead.
            # A run the operator approves with `require_escalated` writes
            # everywhere but the denied paths, git.inc included.
            // optionalAttrs darwin {
              ":tmpdir" = "read";
              ${commandTmpdir} = "write";
            };
          # Commands reach the network only through Codex's proxy, which
          # lets through these domains alone. The proxy runs only while
          # features.network_proxy is on: with it off, as `--disable
          # network_proxy` or a project's config.toml can set, this network
          # is unfiltered. A session whose proxy fails to start fails.
          network =
            {
              enabled = true;
              domains = genAttrs sandboxLists.allowedDomains (_: "allow");
              # Test servers on loopback, as in Claude Code's sandbox.
              allow_local_binding = true;
            }
            # Only the nix daemon's socket, so nix runs in the sandbox; its
            # builds run in the daemon, outside it.
            // optionalAttrs pkgs.stdenv.hostPlatform.isDarwin {
              unix_sockets."/nix/var/nix/daemon-socket/socket" = "allow";
            };
        };
        # Starts the proxy for a profile whose network is enabled; without
        # it, that network is unfiltered.
        features.network_proxy = true;
        # Codex asks the operator, not a reviewer model, when a command
        # needs more than the profile gives; without this, a project Codex
        # does not trust gets no prompts.
        approval_policy = "on-request";
        approvals_reviewer = "user";
        # Commands inherit Codex's whole environment but these.
        shell_environment_policy =
          {
            filters = genAttrs sandboxLists.deniedEnvVars (_: "exclude");
          }
          // optionalAttrs darwin {
            set.TMPDIR = commandTmpdir;
          };
      };

      description = ''
        The permission profile every interactive Codex session's commands
        run in, merged into ~/.codex/config.toml on every switch, so that it
        never rests on what Codex chooses for a trusted project. An
        assertion holds it to denying {option}`wgn.home.llm.sandbox.deniedReads`,
        reaching only {option}`wgn.home.llm.sandbox.allowedDomains`, and
        leaving out {option}`wgn.home.llm.sandbox.deniedEnvVars`. The
        domains hold only while `features.network_proxy` is on: Codex
        0.165 enforces a proxy regardless of it only from an administrator's
        requirements.toml, which a home configuration cannot write.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;
      # Codex runs the review loop too.
      wgn.home.llm.prLoop.enable = mkDefault true;

      # Codex is installed and updated by itself, and writes ~/.codex/config.toml
      # to record project trust, MCP servers and model choice. Generating that
      # file is what stops it being able to.
      home.file =
        llm.mkSkillFiles pkgs ".codex/skills" config.wgn.home.llm.skills
        // roles
        // {
          # Codex skips this when an AGENTS.override.md beside it is non-empty.
          ".codex/AGENTS.md".source = config.wgn.home.llm.instructions;

          # Holds every session to the baseline guard. Codex skips a hook
          # until it is trusted with /hooks, and trust follows the hook's
          # hash, so the command names `pr-loop` on PATH rather than a store
          # path that changes with every rebuild. Codex adds these to any
          # other layer's hooks.
          ".codex/hooks.json".text = builtins.toJSON {
            # Records commits, pushes, worktrees, branches and PRs in the
            # plan and asks which step a commit completes; after a push to
            # GitHub, names the `pr-loop wait ci` to start; hands over the
            # pull-requests skill when a PR is opened or edited, and the
            # commit rules on the session's first commit. Codex names every
            # shell call Bash.
            hooks.PostToolUse = [
              {
                matcher = "Bash";
                hooks = [
                  {
                    type = "command";
                    command = "pr-loop guard --post";
                  }
                ];
              }
              # wait_agent hands the coordinator a sub-agent's result: asks
              # it to check the report and log the part. SubagentStop's
              # output reaches only the sub-agent.
              {
                matcher = "wait_agent";
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan remind";
                  }
                ];
              }
            ];
            # PreCompact can add no context, so after a compaction
            # (source "compact", which agent-plan checks) asks for the plan
            # to be brought up to date. Names the session's plans, matched
            # by Codex session or by worktree, with the brief of the one
            # plan when only one matches.
            hooks.SessionStart = [
              {
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan remind";
                  }
                  {
                    type = "command";
                    command = "agent-plan find --hook --harness codex";
                  }
                ];
              }
            ];
            # The operator's prompt: its URLs become plan references. A
            # sub-agent's turn carries agent_id, which agent-plan skips.
            # Carries the time and how long since the operator's previous
            # message.
            hooks.UserPromptSubmit = [
              {
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan remind";
                  }
                ];
              }
            ];
            hooks.PreToolUse =
              [
                {
                  matcher = ".*";
                  hooks = [
                    {
                      type = "command";
                      command = "pr-loop guard --baseline";
                    }
                  ];
                }
              ]
              ++ optional (cfg.quotaStop != null) {
                matcher = ".*";
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan quota-stop --harness codex --threshold ${toString cfg.quotaStop}";
                  }
                ];
              };
          };
        };
    }

    # Pins interactive sessions' sandbox to the profile above; the guard
    # hook holds the rest of what Codex may do. git delegate's
    # `codex exec --ignore-user-config` reads none of it. Merged into the
    # config.toml Codex writes, whose `default_permissions` wins over a
    # `sandbox_mode` in the same file but not over `--sandbox` or a
    # project's config.toml.
    {
      assertions = [
        {
          assertion = let
            profile = cfg.sandbox.permissions.${cfg.sandbox.default_permissions or ""} or {};
            denied = p: (profile.filesystem.${p} or null) == "deny";
            filters = cfg.sandbox.shell_environment_policy.filters or {};
            coveredByDeny = path: any (p: denied p && codexDenies p path) (attrNames profile.filesystem);
            # Paths under where sops-nix decrypts: git.inc stays readable,
            # and every other one, named or not, is denied.
            secretSamples = let
              root =
                if darwin
                then "/private/var/folders/ab/cd/T/secrets.d"
                else "/run/user/1000/secrets.d";
            in {
              allowed = optional (sandboxLists.allowedReads != []) "${root}/3/rendered/git.inc";
              denied =
                [
                  "${root}/age-keys.txt"
                  "${root}/3/notes-deploy-key"
                  "${root}/3/a-new-secret"
                  "${root}/3/rendered/ssh.inc"
                  "${root}/3/rendered/git.inc.bak"
                  "${root}/3/rendered/git.in"
                  "${root}/3/renderedx/git.inc"
                ]
                ++ optionals (!darwin) [
                  "/run/user/1000/gnupg/S.gpg-agent"
                  "/run/user/1000/bus"
                  "/run/user/1000/secrets.dx"
                  "/run/user/me/secrets.d/3/rendered/git.inc"
                ];
            };
          in
            (profile.extends or null)
            == ":workspace"
            && all denied (subtractLists omittedDenies sandboxLists.deniedReads)
            && !(any denied omittedDenies)
            && all coveredByDeny secretSamples.denied
            && !(any coveredByDeny secretSamples.allowed)
            && !(profile.filesystem ? ${plans.dir})
            && all (v: (filters.${v} or null) == "exclude") sandboxLists.deniedEnvVars
            && (darwin -> (profile.filesystem.":tmpdir" or null) == "read")
            && (profile.network.enabled or null) == true
            && (cfg.sandbox.features.network_proxy or null) == true
            && (profile.network.domains or null) == genAttrs sandboxLists.allowedDomains (_: "allow")
            && hasInfix (escapeShellArg (builtins.toJSON cfg.sandbox)) config.home.activation.codexSandbox.data;
          message = "wgn.home.codex: interactive Codex must run in a profile extending :workspace that denies wgn.home.llm.sandbox.deniedReads and reaches only wgn.home.llm.sandbox.allowedDomains through Codex's proxy, written into config.toml";
        }
      ];

      home.activation.codexSandbox = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${setKeys} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON cfg.sandbox)} \
            || warnEcho "Codex's permission profile was not written."
          ${optionalString darwin "mkdir -p ${escapeShellArg commandTmpdir}"}
        fi
      '';
    }

    # agent-plan's writes need what the profile withholds: the plans
    # directory. Codex keeps a command sandboxed under any allow rule while
    # the profile denies reads, so the operator approves each run instead:
    # a prompt rule asks every time, and outranks an allow rule a past
    # approval saved to default.rules. Approved, a command run with
    # `require_escalated` writes beyond the workspace but still cannot read
    # the denied paths, and the proxy asks again for a host it does not
    # allow.
    {
      home.file.".codex/rules/nix.rules".text =
        concatMapStrings (p: ''
          prefix_rule(pattern=${builtins.toJSON p}, decision="prompt")
        '')
        excludedPrefixes;

      assertions = [
        {
          assertion = let
            rules = config.home.file.".codex/rules/nix.rules".text or "";
          in
            (cfg.sandbox.approval_policy or null)
            == "on-request"
            && (cfg.sandbox.approvals_reviewer or null) == "user"
            && all (p: hasInfix (builtins.toJSON p) rules) excludedPrefixes
            && hasInfix (escapeShellArg (builtins.toJSON cfg.sandbox)) config.home.activation.codexSandbox.data;
          message = "wgn.home.codex: Codex must ask the operator before each of agent-plan's writes in wgn.home.llm.guard.excludedCommands, through a prompt rule in ~/.codex/rules/nix.rules";
        }
      ];
    }

    # pr-loop mcp, as the MCP server agent_ops, the name the guard lets its
    # tools through under (OpsServer in agent-tools' internal/guard): the
    # GitHub and review commands no command in the profile can run. Codex
    # starts it as a process of its own, outside that sandbox, with only
    # its core environment (HOME, PATH, TMPDIR and the like) and the
    # variables env_vars names, a review loop's, which pr-loop holds its
    # writes to. Approval mode prompt asks the operator before every call,
    # and never for the rest of the session; approval_policy and
    # approvals_reviewer above keep the asking with the operator, and a
    # PermissionRequest hook, which could answer for them, is refused.
    # tool_timeout_sec covers a git delegate run of several reviewers.
    # Merged into the config.toml Codex writes, the entry replaced whole.
    (let
      opsServer = {
        command = getExe' config.wgn.home.llm.agentTools "pr-loop";
        args = ["mcp"];
        env_vars = ["PR_LOOP_SESSION" "PR_LOOP_MODE"];
        default_tools_approval_mode = "prompt";
        tool_timeout_sec = 7200;
      };
      servers = builtins.toJSON {agent_ops = opsServer;};
      hooks = builtins.fromJSON config.home.file.".codex/hooks.json".text;
    in {
      home.activation.codexOpsServer = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${registerMcpServers} "$HOME/.codex/config.toml" ${escapeShellArg servers} \
            || warnEcho "pr-loop mcp was not registered with Codex."
        fi
      '';

      assertions = [
        {
          assertion =
            opsServer.default_tools_approval_mode
            == "prompt"
            && !(opsServer ? tools)
            && !(hooks.hooks ? PermissionRequest)
            # hasInfix takes no string naming a store path.
            && hasInfix (unsafeDiscardStringContext (escapeShellArg servers))
            (unsafeDiscardStringContext config.home.activation.codexOpsServer.data);
          message = "wgn.home.codex: Codex must ask the operator before every call to pr-loop mcp's tools: approval mode prompt for the whole server, written into config.toml, and no PermissionRequest hook";
        }
      ];
    })

    # Every interactive session, and each sub-agent started without a role,
    # reasons at high effort, as Claude Code's sub-agents do. Merged into the
    # config.toml Codex writes, so an effort chosen in /model lasts until
    # the next switch.
    {
      home.activation.codexEffort = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${setKeys} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON {model_reasoning_effort = "high";})} \
            || warnEcho "Codex's reasoning effort was not set."
        fi
      '';
    }

    # Near a rate limit Codex offers to switch the running session to a
    # cheaper model, which is the operator's call alone. Merged into the
    # config.toml Codex writes.
    {
      home.activation.codexNotices = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${setNotices} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON {hide_rate_limit_model_nudge = true;})} \
            || warnEcho "Codex's offer to switch models near a rate limit was not hidden."
        fi
      '';
    }

    # Through the ngrok.ai gateway (wgn.home.llm.gateway), interactive Codex
    # and codex-acp use it as their model provider, whose auth command reads
    # the key, so no login and no environment holds it. Memories are off:
    # they run on a built-in OpenAI model the gateway does not serve. So
    # are Codex's analytics, which go to OpenAI, not the gateway.
    # Merged into the config.toml Codex writes.
    (mkIf (config.wgn.home.llm.gateway == "ngrok-ai") (let
      inherit (config.wgn.home.llm) ngrokAi;
      inherit (ngrokAi) provider;
    in {
      home.activation.codexGateway = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${setKeys} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON {
          model_provider = provider.id;
          model = ngrokAi.models.primary;
          review_model = ngrokAi.models.review;
          model_context_window = ngrokAi.contextWindow;
          model_providers.${provider.id} = {
            inherit (provider) name;
            base_url = provider.baseURL;
            wire_api = "responses";
            auth = {
              command = provider.authCommand;
              args = provider.authArgs;
            };
          };
          features.memories = false;
          analytics.enabled = false;
        })} \
            || warnEcho "Codex was not pointed at the ngrok.ai gateway."
        fi
      '';
    }))

    # Merged into the config.toml Codex writes, keeping its formatting and
    # everything else in it. Each named entry is replaced whole.
    # http_headers_helper reads the token when Codex connects.
    (mkIf mcpProxy.enable {
      home.activation.codexMcpServers = lib.hm.dag.entryAfter ["writeBoundary" "mcpProxyToken"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${registerMcpServers} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON (mapAttrs (_: path: {
            url = mcpProxy.url + path;
            http_headers_helper = mcpProxy.headersHelper;
          })
          mcpProxy.endpoints))} \
            || warnEcho "The MCP proxy was not registered with Codex."
        fi
      '';
    })

    # Records the Codex account's quota windows for the quota stop's hook.
    # codex-usage needs Codex's login and the network, which a session's
    # sandbox denies, so it runs here, outside every session.
    (mkIf (cfg.quotaStop != null) (let
      agentPlan = getExe' config.wgn.home.llm.agentTools "agent-plan";
      # Codex installs itself under ~/.local/bin.
      path = "${config.home.homeDirectory}/.local/bin:/usr/bin:/bin";
    in {
      assertions = [
        {
          assertion = config.wgn.home.llm.gateway == "none";
          message = "wgn.home.codex.quotaStop must be null through a gateway (wgn.home.llm.gateway): Codex then spends the gateway's quota, not the OpenAI account's windows codex-usage reads.";
        }
      ];

      launchd.agents.agent-plan-codex-usage = mkIf pkgs.stdenv.hostPlatform.isDarwin {
        enable = true;
        config = {
          ProgramArguments = [agentPlan "codex-usage"];
          RunAtLoad = true;
          StartInterval = 60;
          ProcessType = "Background";
          EnvironmentVariables.PATH = path;
          StandardOutPath = "/tmp/agent-plan-codex-usage.log";
          StandardErrorPath = "/tmp/agent-plan-codex-usage.log";
        };
      };

      systemd.user = mkIf pkgs.stdenv.hostPlatform.isLinux {
        timers.agent-plan-codex-usage = {
          Unit.Description = "Record the Codex account's quota windows every minute";
          Timer = {
            OnActiveSec = "1min";
            OnUnitActiveSec = "1min";
          };
          Install.WantedBy = ["timers.target"];
        };

        services.agent-plan-codex-usage = {
          Unit.Description = "Record the Codex account's quota windows";
          Service = {
            Type = "oneshot";
            Environment = ["PATH=${path}"];
            ExecStart = "${agentPlan} codex-usage";
          };
        };
      };
    }))
  ]);
}
