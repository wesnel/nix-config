{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.claude;

  llm = import ../llm/lib.nix {inherit lib;};

  # The operator's way into a review loop: an ordinary session, held by the
  # host sandbox and the baseline guard like any other, with the loop's mode
  # in its environment, which pr-loop gates its writes on, and pr-loop
  # allowed without a prompt. The guard refuses this command, and any
  # assignment of PR_LOOP_SESSION or PR_LOOP_MODE, to agents. "$defaults"
  # is literal: Claude Code splices its own auto-mode rules in there.
  mkReviewLoop = {postCheck ? ""}:
    pkgs.writeShellApplication {
      name = "claude-review-loop";
      runtimeInputs = [pkgs.jq];
      derivationArgs = {inherit postCheck;};
      text = ''
        usage() {
          echo "usage: claude-review-loop --bots|--people PR..." >&2
          exit 2
        }
        case "''${1-}" in
          --bots) mode=bots ;;
          --people) mode=people ;;
          *) usage ;;
        esac
        shift
        [[ $# -gt 0 ]] || usage
        for pr in "$@"; do
          [[ "$pr" =~ ^#?[1-9][0-9]*$ ]] || usage
        done
        # shellcheck disable=SC2016
        settings=$(jq -c --arg mode "$mode" '.env.PR_LOOP_MODE = $mode' <<<${escapeShellArg (builtins.toJSON {
          env.PR_LOOP_SESSION = "1";
          permissions.allow = ["Bash(pr-loop *)"];
          autoMode.environment = [
            "$defaults"
            "This is the operator's review-loop session over the pull requests it was started for. Its GitHub writes go through `pr-loop`, which holds them to the loop's mode, PR_LOOP_MODE."
          ];
        })})
        exec claude --settings "$settings" --permission-mode auto "/review-loop $*"
      '';
    };

  reviewLoop = mkReviewLoop {postCheck = ": ${reviewLoopTests}";};

  # The wrapper under test, with a claude that prints what it was given.
  reviewLoopTests = let
    fakeClaude = pkgs.writeShellScriptBin "claude" ''
      printf '%s\n' "$@"
    '';
  in
    pkgs.runCommand "claude-review-loop-tests" {nativeBuildInputs = [pkgs.jq];} ''
      export PATH=${fakeClaude}/bin:$PATH
      out_=$(${getExe (mkReviewLoop {})} --people 12 '#13')
      settings=$(sed -n 2p <<<"$out_")
      jq -e '.env == {PR_LOOP_SESSION: "1", PR_LOOP_MODE: "people"} and .permissions.allow == ["Bash(pr-loop *)"]
        and .autoMode.environment[0] == "$defaults"' <<<"$settings"
      [[ "$(sed -n 1p <<<"$out_")" == --settings && "$(sed -n 3,4p <<<"$out_")" == $'--permission-mode\nauto' ]]
      [[ "$(sed -n 5p <<<"$out_")" == "/review-loop 12 #13" ]]
      ${getExe (mkReviewLoop {})} --bots 7 | sed -n 2p | jq -e '.env.PR_LOOP_MODE == "bots"'
      for bad in "" "--both 1" "--bots" "--bots x"; do
        # shellcheck disable=SC2086
        if ${getExe (mkReviewLoop {})} $bad 2>/dev/null; then
          echo "claude-review-loop accepted '$bad'" >&2
          exit 1
        fi
      done
      touch $out
    '';

  # The role agents, plain agent files in ./agents: the sub-agents the
  # coordinate-subagents skill starts, linked into ~/.claude/agents, and the
  # headless reviewers git delegate runs from review-agents.json. A
  # reviewer's file names its tools and the hook that keeps it to reading,
  # `@guard@ guard --reviewer`, @guard@ being pr-loop, with `onFailure:
  # block`, which Claude Code's frontmatter hooks take as its settings'
  # hooks do. Each prompt ends with
  # the rules every sub-agent shares, then its contract, if any. A
  # description is plain YAML: no colon followed by a space. Each focused
  # review, a route of agent-tools' routes.Known, has a reviewer of its
  # own, ROUTE-reviewer, whose contract is that focus's.
  focusReviewers = map (route: "${route}-reviewer") (attrNames pkgs.agent-tools.focusPrompts);
  agentNames = ["builder" "adversarial-reviewer" "design-reviewer" "review-guide" "diagrammer"] ++ focusReviewers;
  contracts = mapAttrs' (route: nameValuePair "${route}-reviewer") pkgs.agent-tools.focusPrompts;
  reviewerHook = "${getExe' config.wgn.home.llm.agentTools "pr-loop"} guard --reviewer";

  # An agent file's frontmatter, as lines, and its prompt.
  agentFile = name: let
    raw = builtins.readFile (./agents + "/${name}.md");
    text = replaceStrings ["@guard@"] [(getExe' config.wgn.home.llm.agentTools "pr-loop")] raw;
    pieces = splitString "\n---\n" (removePrefix "---\n" text);
    frontmatter = splitString "\n" (head pieces);
    field = key: let
      lines = filter (hasPrefix "${key}: ") frontmatter;
    in
      if lines == []
      then null
      else removePrefix "${key}: " (head lines);
    # The `skills:` list, one `  - NAME` line per skill, read from the file
    # as written: a name taken from `text` carries pr-loop's store path,
    # which an attribute name cannot.
    skills = let
      lines = splitString "\n" (head (splitString "\n---\n" (removePrefix "---\n" raw)));
      start = lists.findFirstIndex (line: line == "skills:") null lines;
      after =
        if start == null
        then []
        else drop (start + 1) lines;
    in
      map (removePrefix "  - ") (take (lists.findFirstIndex (line: !hasPrefix "  - " line) (length after) after) after);
  in
    assert assertMsg (hasPrefix "---\n" text && length pieces > 1) "claude: ./agents/${name}.md has no frontmatter";
    assert assertMsg (field "name" == name) "claude: ./agents/${name}.md is not named ${name}";
    assert assertMsg (field "tools" == null || any (hasSuffix "command: \"${reviewerHook}\"") frontmatter)
    "claude: ./agents/${name}.md restricts its tools but runs no `guard --reviewer` hook";
    assert assertMsg (field "tools" == null || any (hasSuffix " onFailure: block") frontmatter)
    "claude: ./agents/${name}.md's `guard --reviewer` hook lacks `onFailure: block`, so a hook that cannot run would let the call through"; {
      inherit field skills;
      text = text + "\n" + builtins.readFile ./agents/subagent.md + (contracts.${name} or "");
      prompt = concatStringsSep "\n---\n" (tail pieces) + "\n" + builtins.readFile ./agents/subagent.md + (contracts.${name} or "");
    };

  # Through a gateway, the adversarial reviewer, the second opinion when git
  # delegate cannot run Codex, runs on the gateway's review model, of
  # another family than the author's.
  agentText = name: let
    text = (agentFile name).text;
    opus = "\nmodel: opus\n";
  in
    if name == "adversarial-reviewer" && gateway.gateway == "ngrok-ai"
    then
      assert assertMsg (hasInfix opus text) "claude: ./agents/${name}.md no longer says `model: opus`, which the gateway's review model replaces";
        replaceStrings [opus] ["\nmodel: ${gateway.ngrokAi.models.review}\n"] text
    else text;

  roleAgents = listToAttrs (map (name: nameValuePair ".claude/agents/${name}.md" {text = agentText name;}) agentNames);

  gateway = config.wgn.home.llm;

  # What points Claude Code, and the headless reviewers through
  # reviewer-settings.json, at the gateway: its address, a command that
  # prints its key (sent as both x-api-key and Bearer), the model aliases,
  # and a context window for model IDs Claude Code does not recognize.
  # Never ANTHROPIC_API_KEY, which agent-shell sets empty.
  gatewaySettings = optionalAttrs (gateway.gateway == "ngrok-ai") (let
    inherit (gateway.ngrokAi) models;
  in {
    apiKeyHelper = escapeShellArgs gateway.ngrokAi.keyCommand;
    env = {
      ANTHROPIC_BASE_URL = gateway.ngrokAi.baseURL;
      ANTHROPIC_DEFAULT_OPUS_MODEL = models.primary;
      ANTHROPIC_DEFAULT_SONNET_MODEL = models.primary;
      ANTHROPIC_DEFAULT_FABLE_MODEL = models.primary;
      ANTHROPIC_DEFAULT_HAIKU_MODEL = models.fast;
      CLAUDE_CODE_MAX_CONTEXT_TOKENS = toString gateway.ngrokAi.contextWindow;
      DISABLE_TELEMETRY = "1";
    };
  });

  principles = import ../llm/principles.nix;
  # A principle skill's text without its frontmatter.
  principleBody = name: concatStringsSep "\n---\n" (tail (splitString "\n---\n" (removePrefix "---\n" (builtins.readFile principles.${name}))));

  # The reviewers git delegate runs headless, with `--agents` and this file,
  # which outranks a reviewed project's own agents. Each prompt ends with
  # the text of the principles its agent file's `skills:` lists, so a
  # reviewer run with `--setting-sources ""` has them whether or not it
  # finds skills by name; the agent file installed for sub-agents keeps
  # them as `skills:`.
  reviewAgents = pkgs.runCommand "review-agents.json" {
    nativeBuildInputs = [pkgs.jq];
    json = builtins.toJSON (genAttrs (focusReviewers ++ ["review-guide" "diagrammer"]) (name: let
      a = agentFile name;
    in {
      description = a.field "description";
      prompt = assert assertMsg (all (skill: principles ? ${skill}) a.skills)
      "claude: ./agents/${name}.md lists a skill that is not a principle, which its headless prompt would leave out";
        a.prompt + concatMapStrings (skill: "\n" + principleBody skill) a.skills;
      tools = splitString ", " (a.field "tools");
      model = a.field "model";
      hooks.PreToolUse = [
        {
          matcher = "*";
          hooks = [
            {
              type = "command";
              command = reviewerHook;
              # A hook that cannot run denies the call.
              onFailure = "block";
            }
          ];
        }
      ];
    }));
  } ''jq -e . <<<"$json" > $out'';

  # The headless reviewers' only settings, since git delegate passes
  # `--setting-sources ""`: the host sandbox's credentials, denied reads and
  # network, a sandbox no project can loosen, and no exclusions, allow rules
  # or hooks; through a gateway, its settings too. Each reviewer's hook
  # rides in its agent entry.
  reviewerSettings =
    pkgs.runCommand "reviewer-settings.json" {
      nativeBuildInputs = [pkgs.jq];
      json = builtins.toJSON ({
          sandbox = {
            enabled = true;
            failIfUnavailable = true;
            allowUnsandboxedCommands = false;
            filesystem.denyRead = hostSandbox.filesystem.denyRead;
            network.allowedDomains = hostSandbox.network.allowedDomains;
          };
          permissions.deny = credentialReads;
        }
        // gatewaySettings);
    } ''
      jq -e . <<<"$json" > $out
      jq -e '.sandbox.excludedCommands == null and .permissions.allow == null and .hooks == null' $out > /dev/null
    '';

  # Read is not hooked, so credentials are denied it by rule: each path a
  # prefix, so one rule matches a file whose name it starts, and another
  # anything under a directory whose name it starts.
  credentialReads = concatMap (p: ["Read(${rulePath p}*)" "Read(${rulePath p}*/**)"]) config.wgn.home.llm.guard.credentialPaths;

  # The guard's mcp.json as permission rules, for each server of each
  # service: its reads allowed and its confirmed writes asked, without a
  # hook round trip. The hook still sees every MCP call (its matcher keeps
  # `mcp__.*`) and refuses a listed server's other tools.
  mcpRules = let
    services = attrValues pkgs.agent-tools.mcpTools;
    rules = key: concatMap (s: concatMap (server: map (tool: "mcp__${server}__${tool}") s.${key}) s.servers) services;
  in {
    allow = rules "reads";
    ask = rules "confirms";
  };

  darwin = pkgs.stdenv.hostPlatform.isDarwin;
  # A clone's agent settings, in its common git directory, wherever the
  # clone is (internal/repos).
  agentSettingsWrites = "/**/.git/info/agents.json";
  mcpProxy = config.wgn.home.mcp-proxy;
  plans = config.wgn.home.llm.plans;
  agentPlan = getExe' config.wgn.home.llm.agentTools "agent-plan";

  # The quota stop's PreToolUse entry, for every Claude Code settings file
  # this module writes. It stops a session near the account's quota, from
  # the windows the status line records, and sees every tool, letting
  # through only those that read, ask or hand back. With no record it can
  # read it denies nothing; a hook that cannot run denies, as every
  # PreToolUse hook here does. A deny from it wins over the guard's ask.
  quotaStopHooks = optional (cfg.quotaStop != null) {
    matcher = "*";
    hooks = [
      {
        type = "command";
        command = "${agentPlan} quota-stop --threshold ${toString cfg.quotaStop}";
        onFailure = "block";
      }
    ];
  };

  # Review records, internal/record's Dir(): only the excluded writers,
  # which compute each path from what they review, change them.
  records = "${config.xdg.stateHome}/agent-tools/reviews";

  # A permission rule's path: Claude Code reads `~/` as the home directory,
  # and a lone `/` as relative to the settings file, so `//` starts an
  # absolute one.
  rulePath = path: let
    home = "${config.home.homeDirectory}/";
  in
    if hasPrefix home path
    then "~/${removePrefix home path}"
    else "/${path}";

  sandboxLists = config.wgn.home.llm.sandbox;

  # `gh` for the host sandbox's excluded commands, which run with the
  # operator's login and network: only gh's reads of pull requests,
  # workflow runs and repositories, and never `--web`, which opens a
  # browser. A short flag cluster holding `w` is `--web` too, but in `run
  # list`, whose -w is --workflow. Its arguments may name any host gh takes
  # (`-R HOST/OWNER/REPO`, a pull request's URL).
  #
  # The session can write the working directory's git config, and gh runs
  # git, so gh never runs where git would find that repository: it runs in
  # an empty temporary directory, with no system or global git config, no
  # GIT_* variable of the session's, and GIT_CEILING_DIRECTORIES keeping
  # git from looking above it. gh gets no GH_* variable of the session's
  # either (GH_REPO, GH_HOST, GH_CONFIG_DIR, GH_TOKEN), so the operator's
  # own gh config and login are what it reads with. Where the arguments
  # name no repository,
  # gh-read names the working directory's base repository as gh would pick
  # it, from its remotes' URLs, and where `pr view`, `checks` or `diff`
  # names no pull request, the branch checked out there: `git config` and
  # `git symbolic-ref` read them without running anything. Neither may
  # start with -, which gh would read as a flag.
  mkGhRead = {
    gh,
    postCheck ? "",
  }:
    pkgs.writeShellApplication {
      name = "gh-read";
      runtimeInputs = [pkgs.coreutils pkgs.git];
      derivationArgs = {inherit postCheck;};
      text = ''
        refuse() {
          echo "gh-read: refused: $1" >&2
          exit 2
        }

        case "''${1-} ''${2-}" in
          "pr view" | "pr list" | "pr diff" | "pr checks" | "pr status") ;;
          "run view" | "run list" | "run watch" | "repo view") ;;
          *) refuse "gh-read runs only gh pr view|list|diff|checks|status, gh run view|list|watch and gh repo view" ;;
        esac

        # Each subcommand's short flags that take a value, as gh 2.102's
        # `gh SUBCOMMAND --help` lists them, with -R where gh takes it; and
        # whether its -w is --web (run list's is --workflow). Every other
        # short flag is a switch.
        case "$1 $2" in
          "pr view") short=Rqt web=1 ;;
          "pr checks") short=Riqt web=1 ;;
          "pr diff") short=Re web=1 ;;
          "pr list") short=RaABHlLSsqt web=1 ;;
          "pr status") short=Rqt web="" ;;
          "run view") short=Rajqt web=1 ;;
          "run list") short=RbceLqstuw web="" ;;
          "run watch") short=Ri web="" ;;
          "repo view") short=bqt web=1 ;;
        esac
        # The long flags of those subcommands that take a value, given
        # after = or as the next argument.
        long=" --repo --jq --json --template --interval --color --exclude --app --assignee --author --base --head --label --limit --search --state --attempt --job --branch --commit --created --event --status --user --workflow "

        # named: the arguments name a repository (-R, a URL, or `repo
        # view`'s argument). positional: they hold an argument that is no
        # flag's (a pull request's number, branch or URL). value: the next
        # argument is a flag's value.
        named=""
        positional=""
        value=""
        for arg in "''${@:3}"; do
          if [[ -n "$value" ]]; then
            value=""
            continue
          fi
          case "$arg" in
            --)
              positional=1
              break
              ;;
            --web | --web=*) refuse "--web opens a browser; leave it out and read what gh-read prints" ;;
            --repo) named=1 value=1 ;;
            --repo=*) named=1 ;;
            --*=*) ;;
            --*)
              if [[ "$long" == *" $arg "* ]]; then
                value=1
              fi
              ;;
            -?*)
              # Short flags as gh reads a cluster of them: each letter a
              # flag, up to one that takes a value, which takes the rest of
              # the argument or, where nothing is left, the next one.
              flags=''${arg#-}
              while [[ -n "$flags" ]]; do
                f=''${flags:0:1}
                flags=''${flags:1}
                if [[ "$f" == w && -n "$web" ]]; then
                  refuse "$arg holds -w, which opens a browser; leave it out and read what gh-read prints"
                fi
                if [[ "$short" == *"$f"* ]]; then
                  if [[ "$f" == R ]]; then
                    named=1
                  fi
                  if [[ -z "$flags" ]]; then
                    value=1
                  fi
                  break
                fi
              done
              ;;
            *://*) named=1 positional=1 ;;
            *)
              positional=1
              if [[ "$1" == repo ]]; then named=1; fi
              ;;
          esac
        done

        for name in "''${!GIT_@}" "''${!GH_@}"; do
          unset "$name"
        done

        case "$1 $2" in
          "pr view" | "pr checks" | "pr diff")
            if [[ -z "$positional" ]]; then
              branch=$(git symbolic-ref --short -q HEAD 2>/dev/null) ||
                refuse "the working directory has no branch checked out to find its pull request by; name the pull request, as a number, branch or URL"
              if [[ "$branch" == -* ]]; then
                refuse "the branch checked out, $branch, starts with -, which gh would read as a flag; name the pull request, as a number or URL"
              fi
              set -- "$@" "$branch"
            fi
            ;;
        esac

        if [[ -z "$named" ]]; then
          # The base repository, as gh picks it: the remote `gh repo
          # set-default` marked (remote.NAME.gh-resolved = base), else
          # upstream, else origin.
          remote=origin
          if git config --get remote.upstream.url >/dev/null; then
            remote=upstream
          fi
          while read -r key resolved; do
            if [[ "$resolved" == base ]]; then
              remote=''${key#remote.}
              remote=''${remote%.gh-resolved}
            fi
          done < <(git config --get-regexp '^remote\..*\.gh-resolved$' || true)
          name='[A-Za-z0-9_.-]+'
          host='[A-Za-z0-9][A-Za-z0-9.-]*'
          origin=$(git config --get "remote.$remote.url") ||
            refuse "the working directory has no remote.$remote.url to name its repository by; name it, as -R OWNER/REPO, or as \`gh-read repo view OWNER/REPO\`"
          origin=''${origin%/}
          origin=''${origin%.git}
          if [[ "$origin" =~ ^[a-z+]+://([^/@]+@)?($host)(:[0-9]+)?/($name)/($name)$ ]]; then
            repo=''${BASH_REMATCH[2]}/''${BASH_REMATCH[4]}/''${BASH_REMATCH[5]}
          elif [[ "$origin" =~ ^([^/@:]+@)?($host):($name)/($name)$ ]]; then
            repo=''${BASH_REMATCH[2]}/''${BASH_REMATCH[3]}/''${BASH_REMATCH[4]}
          else
            refuse "remote.$remote.url $origin names no HOST/OWNER/REPO; name the repository, as -R OWNER/REPO"
          fi
          if [[ "$1" == repo ]]; then
            set -- "$@" "$repo"
          else
            set -- "$1" "$2" -R "$repo" "''${@:3}"
          fi
        fi

        tmp=$(mktemp -d)
        tmp=$(cd "$tmp" && pwd -P)
        trap 'rm -rf -- "$tmp"' EXIT
        cd "$tmp"
        GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CEILING_DIRECTORIES=$(dirname "$tmp") ${getExe gh} "$@"
      '';
    };

  ghRead = mkGhRead {
    gh = pkgs.gh;
    postCheck = ": ${ghReadTests}";
  };

  ghReadTests = let
    # Prints what gh-read passed on, where it ran, and what the git it
    # leaves gh finds there.
    fakeGh = pkgs.writeShellScriptBin "gh" ''
      printf 'args:'
      printf ' [%s]' "$@"
      echo
      echo "pwd: $PWD"
      echo "global: ''${GIT_CONFIG_GLOBAL-}"
      env | while IFS= read -r line; do
        if [[ "$line" == GH_* ]]; then echo "env: $line"; fi
      done
      git status >/dev/null 2>&1 && echo "git status: found a repository"
      git remote -v 2>/dev/null | while IFS= read -r line; do echo "remote: $line"; done
      git st >/dev/null 2>&1 && echo "git st: ran"
      true
    '';
    underTest = mkGhRead {gh = fakeGh;};
    run = pkgs.writeShellApplication {
      name = "gh-read-tests";
      runtimeInputs = [pkgs.gnugrep pkgs.coreutils pkgs.git];
      text = builtins.readFile ./gh-read-tests.sh;
    };
  in
    pkgs.runCommand "gh-read-tests" {} ''
      ${getExe run} ${getExe underTest}
      touch $out
    '';

  # agent-plan's subcommands that write a plan's files, its log or its lock,
  # run outside the sandbox, which denies the plans directory to every other
  # command. Never sync, init or reseal: sync pushes and init clones with the
  # notes deploy key, and reseal is the operator's.
  agentPlanWrites = [
    "new"
    "log"
    "ask"
    "decide"
    "followup"
    "reference"
    "postpone"
    "set"
    "own"
    "sign-id"
    "sign-result"
    "step"
    "part"
    "edit"
    "after"
    "section"
  ];

  # pr-loop's subcommands that need the operator's GitHub login, or write
  # the review records the sandbox denies (threads, draft and post). Never
  # setup, which runs a command a checkout's tools decide, or setup-command
  # and guard, which work in the sandbox. post is the operator's: the guard
  # refuses it to agents, and it asks on /dev/tty.
  prLoopGitHub = ["open" "stack" "link" "push" "threads" "reply" "resolve" "draft" "post" "request" "status" "ci" "logs" "wait"
    "sign-check" "end"];

  # The read-only ones, which run without a prompt in an ordinary session;
  # threads writes only its PR's review record. The writes are left to the
  # permission flow.
  prLoopReads = ["stack" "threads" "status" "ci" "logs" "wait"];

  # A trailing ` *` also matches the bare command (permissions'
  # wildcard-patterns), so one entry per subcommand covers it with and
  # without arguments.
  sub = command: name: "${command} ${name} *";

  # What the host sandbox denies reading beyond the credentials, which the
  # guard reads from wgn.home.llm.guard.credentialPaths too: what would let
  # a command act as the operator elsewhere. The guard refuses a .env file
  # by its name instead.
  hostDeniedReads = [
    "~/git/**/.env"
    "~/git/**/.env.*"
    "~/.claude/worktrees/**/.env"
    "~/.claude/worktrees/**/.env.*"
    # Browser profiles, 1Password and shell histories.
    "~/Library/Application Support/Google/Chrome"
    "~/Library/Application Support/BraveSoftware"
    "~/Library/Application Support/Firefox"
    "~/Library/Safari"
    "~/Library/Containers/com.apple.Safari"
    "~/.config/google-chrome"
    "~/.config/chromium"
    "~/.mozilla"
    "~/Library/Application Support/zen"
    "~/.zen"
    "~/.config/zen"
    "~/.var/app/app.zen_browser.zen"
    "~/Library/Application Support/1Password"
    "~/Library/Group Containers/2BUA8C4S2C.com.1password"
    "~/.config/1Password"
    "~/.local/share/fish/fish_history"
    "~/.zsh_history"
    "~/.zsh_sessions"
    "~/.bash_history"
    # Only the watcher's sync pushes plans.
    plans.repository.key
  ];

  # The credential list, as the sandbox takes paths: no trailing `/`, and
  # none that is or is under a directory sandboxLists.deniedReads hides
  # whole, such as the `~/.ssh/id_` prefix.
  sandboxCredentials = let
    hidden = p: any (d: p == d || hasPrefix "${d}/" p) sandboxLists.deniedReads;
  in
    filter (p: !hidden p) (map (removeSuffix "/") config.wgn.home.llm.guard.credentialPaths);

  # The sandbox around every session's commands. Paths starting `~/` are
  # resolved by Claude Code; bare relative ones would resolve against
  # ~/.claude, not the workspace.
  hostSandbox = {
    enabled = true;
    allowUnsandboxedCommands = false;
    failIfUnavailable = true;

    # A call made of nothing but these runs unsandboxed, with the operator's
    # login and network, after the usual permission check; piped or chained
    # with anything else, redirected, or given a command substitution, it
    # may stay sandboxed and fail, so the guard refuses such a line
    # (wgn.home.llm.guard.excludedCommands). A pattern's `*` matches every
    # argument that follows, so a pattern is safe only where no argument can
    # make the command write beyond what it is for or run a program: no `gh`
    # itself
    # (`gh api` writes, and `gh pr view --web` runs a browser), no nix (any
    # flake the session can edit, or --override-input, reads any path the
    # operator can, and the daemon builds with the network on macOS), no
    # emacsclient (the daemon evaluates whatever it is sent, and a socket
    # path the session can write may be a link to the operator's), never
    # `pr-loop *`.
    #
    # - `gh-read` runs only gh's reads and refuses `--web`, so it reads with
    #   the operator's gh login and prints what it read: pull requests, their
    #   diffs and checks, workflow runs and logs, and repositories, private
    #   ones included, from whichever host its arguments name.
    # - git delegate: Codex's own sandbox cannot start inside this one, a
    #   pull request is fetched with the operator's login, and it writes the
    #   review records the sandbox denies. api.openai.com stays out of the
    #   allowed domains, since the command runs outside the sandbox anyway.
    #   Every delegate subcommand is one an agent may run; an operator-only
    #   action must never become one, since this exclusion and its allow
    #   rule below take them all.
    # - pr-loop's GitHub commands and agent-plan's writes: neither takes a
    #   program or a host from its arguments. The guard refuses a file
    #   argument that names a credential (wgn.home.llm.guard.credentialPaths,
    #   which denyRead holds too), and pr-loop refuses to post text holding a
    #   secret's shape.
    excludedCommands =
      ["gh-read *" "git delegate *" "git-delegate *"]
      ++ map (sub "pr-loop") prLoopGitHub
      ++ map (sub "agent-plan") agentPlanWrites;

    # Commands inherit Claude Code's environment, and only the variables
    # named here are taken out of it, so every credential a module puts in
    # the environment belongs here.
    credentials.envVars =
      map (name: {
        inherit name;
        mode = "deny";
      }) [
        "GH_TOKEN"
        "GITHUB_TOKEN"
        "SSH_AUTH_SOCK"
        "KEYID"
        "NGROK_AUTHTOKEN"
        "NGROK_AUTHTOKEN_PROD"
        "NGROK_AI_KEY"
        "MCP_PROXY_TOKEN"
        "OPENAI_API_KEY"
        "ANTHROPIC_API_KEY"
        "CLAUDE_CODE_MESSAGING_TOKEN"
      ];

    # Go programs, gh and the go command among them, verify TLS through
    # trustd on macOS, which they fail to reach otherwise. This opens the
    # trust service alone, not the keychain.
    enableWeakerNetworkIsolation = darwin;

    network =
      {
        # A command can send whatever it can read to any of these, with any
        # credential the session holds itself, such as a token in the
        # workspace or in a command's output: pushing, uploading a release or
        # creating a gist on github.com under that token's account.
        allowedDomains =
          [
            "github.com"
            "*.github.com"
            "*.githubusercontent.com"
            "git.sr.ht"
            "cache.nixos.org"
            "channels.nixos.org"
            "vuln.go.dev"
            # ngrok agent releases and packages.
            "bin.ngrok.com"
            "apt.ngrok.com"
            "ngrok-agent.s3.amazonaws.com"
          ]
          ++ sandboxLists.allowedDomains;
        strictAllowlist = true;
        # Lets the session's commands serve and reach test servers on macOS;
        # Linux gives each command its own loopback instead. It opens every
        # service listening on loopback as well, to whatever that service
        # does for a caller without credentials:
        #
        # - mcp-proxy: closed; it needs its token, which the sandbox hides.
        # - The operator's own applications, outside nix-config: Okta
        #   Verify, Notion, lima's port forwards, Parallels, and macOS's
        #   AirPlay receiver among them.
        allowLocalBinding = true;
      }
      # Only the nix daemon's socket, so nix runs in the sandbox. Its client
      # evaluates inside the sandbox, but every build, including one an
      # evaluation imports from, runs in the daemon outside it: on macOS
      # without a sandbox of its own and with the network, so a command that
      # can define a derivation can reach any host. No other socket may go
      # here.
      # Linux cannot allow one socket, so the agent sockets are hidden below.
      // (
        if darwin
        then {allowUnixSockets = ["/nix/var/nix/daemon-socket/socket"];}
        else {allowAllUnixSockets = true;}
      );

    filesystem = {
      denyRead = sandboxLists.deniedReads ++ sandboxCredentials ++ hostDeniedReads;

      # Git reads the user's name and email from this file on every run.
      allowRead = optional (config.sops.templates ? "git.inc") (
        if darwin
        then "/private/var/folders/*/*/T/secrets.d/*/rendered/git.inc"
        else "/run/user/*/secrets.d/*/rendered/git.inc"
      );

      # Nothing on PATH (~/go/bin among it) and nothing that a program
      # outside the sandbox loads code or configuration from. Go's and
      # golangci-lint's own caches, where they are by default. `trash` moves
      # a file on the home volume into ~/.Trash, which the trash skill asks
      # for in place of rm.
      allowWrite =
        ["~/go/pkg"]
        ++ (
          if darwin
          then ["~/Library/Caches/go-build" "~/Library/Caches/golangci-lint" "~/.Trash"]
          else ["~/.cache/go-build" "~/.cache/golangci-lint"]
        )
        ++ ["~/.claude/worktrees" "~/.cache/nix" "~/.cache/uv"];

      # The plans directory holds the plans, their logs and the logs'
      # locks, which only agent-plan's excluded writes change; the notes
      # repository's git directory, so that no git command run in the
      # repository can reset, switch, clean or merge the plans either. The
      # review records, which the excluded writers alone change.
      denyWrite =
        [plans.dir "${plans.repository.path}/.git" records]
        # Every clone's agent settings, which the operator alone writes. Only
        # macOS's sandbox takes a wildcard in a write list; Linux's skips the
        # entry, which leaves the guard to refuse the write there.
        ++ optional darwin agentSettingsWrites;
    };
  };

in {
  options.wgn.home.claude = {
    enable = mkEnableOption "Enables my Claude Code skills and context for home-manager";

    agentPlans = {
      enable = mkOption {
        type = types.bool;
        default = false;

        description = ''
          Linux only. Runs the Agent plans page's session as
          `agent-plans.service`: `claude remote-control` in
          `agent-plans/session` under {option}`xdg.stateHome`, published
          from the devbox, held by the baseline guard, with its own Claude
          Code config in `agent-plans/claude`, whose settings it cannot
          change. The page is {option}`wgn.home.llm.agentPlans.claudePage`;
          started without it, the session publishes the page and gives its
          URL to set there. Before the first start, log that
          config in and trust the session's folder
          once: in `agent-plans/session`, run
          `CLAUDE_CONFIG_DIR=~/.local/state/agent-plans/claude claude`,
          `/login`, accept the folder, and quit. The session folder is
          writable by any process running as the operator; only managed
          settings, which need root, would be a wall. The kill switch is
          its own: create `agent-plans/off` under {option}`xdg.stateHome`,
          then `systemctl --user stop agent-plans.service`. Needs Claude
          Code signed in.
        '';
      };
    };

    quotaStop = mkOption {
      type = types.nullOr (types.ints.between 1 100);
      # A gateway sends no quota windows for the status line to record.
      default =
        if config.wgn.home.llm.gateway == "none"
        then 90
        else null;
      defaultText = literalExpression ''if config.wgn.home.llm.gateway == "none" then 90 else null'';

      description = ''
        The share of a Claude quota window, in percent, at which every Claude
        Code session stops. While a window that `agent-plan statusline`
        recorded in the last five minutes, and that has not reset, is at or
        above it, the PreToolUse hook `agent-plan quota-stop` refuses
        starting sub-agents and every tool call but reading, asking the
        operator, stopping sub-agents, handing back and `agent-plan` alone
        on its line. Nothing in a session lifts the stop; null turns it off
        at the next switch. Codex sessions spend another quota and are not
        stopped. Off by default through a gateway
        ({option}`wgn.home.llm.gateway`), which reports no quota windows.
      '';
    };

    settings = mkOption {
      type = types.attrs;

      description = ''
        Keys merged into ~/.claude/settings.json on every switch. The file stays
        Claude Code's, so settings changed from inside it are kept, but a key
        set here returns to this value at the next switch.
      '';

      default = {
        # Shared instructions forbid attribution trailers; this stops Claude
        # Code adding them, in the form every version accepts.
        attribution = {
          commit = "";
          pr = "";
          sessionUrl = false;
        };

        # Credentials stay out of every session. The guard denies shell
        # commands and file tools naming them, and Read is denied them here.
        permissions.deny =
          credentialReads
          # A plan's log is appended to by `agent-plan log` alone; the guard
          # denies the same files to every file tool and shell write. An Edit
          # rule covers Write too.
          ++ map (p: "Edit(${rulePath plans.dir}/${p})") ["*.log.org" "*.log.org.lock"]
          # Every plan file is agent-plan's to change, which the sandbox
          # holds for shell commands and this for file tools, which run
          # outside it, where the guard's hook cannot run. Review records
          # likewise.
          ++ ["Edit(${rulePath plans.dir}/**)" "Edit(${rulePath records}/**)"]
          # Every clone's agent settings, for the file tools, wherever the
          # clone is. The guard refuses the same writes.
          ++ ["Edit(${rulePath agentSettingsWrites})"]
          # The usual spellings of the operator's actions, each a policy.json
          # baseline row, refused even where the hook cannot run. Rules are
          # not a boundary: the guard reads every other spelling, and the
          # sandbox holds the credentials these need.
          ++ map (c: "Bash(${c} *)") ["gh pr merge" "gh pr close" "gh pr reopen" "gh pr review" "gh pr comment"
            "gh release" "gh repo delete" "git push" "sudo" "darwin-rebuild switch" "nix profile install" "npm publish"
            "cargo publish"];

        # The guard refuses marking a pull request ready; its --undo, back
        # to draft, asks.
        permissions.ask = ["Bash(gh pr ready *)"] ++ mcpRules.ask;

        # A nested session started to skip every prompt. The guard refuses
        # `claude --dangerously-skip-permissions` too, for a launch that
        # reads another settings file.
        permissions.disableBypassPermissionsMode = "disable";

        # Excluded commands go through the permission flow. These run
        # without a prompt or auto mode's classifier: the excluded commands
        # that only read, and every agent-plan write. A rule allows a call
        # only when every command in it matches.
        permissions.allow =
          map (s: "Bash(${sub "pr-loop" s})") prLoopReads
          # Every delegate subcommand is one an agent may run; an
          # operator-only action must never become one, since this rule
          # allows it.
          ++ ["Bash(git delegate *)" "Bash(git-delegate *)"]
          ++ map (w: "Bash(${sub "agent-plan" w})") agentPlanWrites
          ++ mcpRules.allow;

        # The auto-mode classifier judges intent, so it also stops a rule
        # broken where the guard cannot see, as through a script; the guard
        # stays the enforcement. "$defaults" is literal: Claude Code splices
        # its own rules in there.
        autoMode.hard_deny = [
          "$defaults"
          "Reading credentials: SSH or GPG private keys, agents' logins, keychain items, tokens on disk, or the environment's secrets."
          "Pushing to GitHub, or changing anything there (pull requests, issues, comments, reviews, releases, settings), other than through `pr-loop`."
          "Running anything as root: sudo, doas, su, or an administrator prompt."
          "Changing an agent's settings or hooks, or the guard's config: Claude Code's or Codex's settings and hooks files, or agent-tools/config.json; or turning hooks or permission checks off."
          "Publishing a package, container image or release."
          "Running a command with NGROK_ENV set to prod or stage, however it is set."
        ];

        # Every session's commands, a review loop's included.
        sandbox = hostSandbox;

        hooks.PostToolUse = [
          # Records commits, pushes, worktrees, branches and PRs in the plan
          # and asks which step a commit completes; after a push to GitHub,
          # names the `pr-loop wait ci` to start; hands over the
          # pull-requests skill when a PR is opened or edited, and the
          # commit rules on the session's first commit.
          {
            matcher = "Bash";
            hooks = [
              {
                type = "command";
                command = "${getExe' config.wgn.home.llm.agentTools "pr-loop"} guard --post";
              }
            ];
          }
          # A foreground sub-agent's result: asks the coordinator to check
          # it and log the part. A background one's arrives as a prompt.
          {
            matcher = "Agent|Task";
            hooks = [
              {
                type = "command";
                command = "${agentPlan} remind";
              }
            ];
          }
        ];

        # A background sub-agent's task-notification reaches the coordinator
        # as a prompt; SubagentStop's output would reach only the sub-agent.
        # Any other prompt is the operator's: its URLs become plan references.
        # Either carries the time and how long since the operator's previous
        # message.
        hooks.UserPromptSubmit = [
          {
            hooks = [
              {
                type = "command";
                command = "${agentPlan} remind";
              }
            ];
          }
        ];

        # Its output becomes the compaction's instructions: keep what the
        # plan does not yet record. Exit 2 would block the compaction.
        hooks.PreCompact = [
          {
            hooks = [
              {
                type = "command";
                command = "${agentPlan} remind";
              }
            ];
          }
        ];

        # Names the session's plans when it resumes, compacts or is
        # cleared, matched by its session_id, which all three keep, or by
        # its worktree; after /clear or a compaction, with
        # `agent-plan brief --resume` of the first, which stays within the
        # 10,000 characters Claude Code passes whole.
        hooks.SessionStart =
          [
            {
              matcher = "clear|compact|resume";
              hooks = [
                {
                  type = "command";
                  command = "${agentPlan} find --hook";
                }
              ];
            }
          ]
          # Names each mcp-proxy server that is not connected when the
          # session starts, which Claude Code reports only as not found,
          # with the command that connects it, so the session asks the
          # operator rather than adding a server of its own.
          ++ optional mcpProxy.enable {
            matcher = "startup";
            hooks = [
              {
                type = "command";
                command = "${getExe' config.wgn.home.llm.agentTools "pr-loop"} mcp-status --hook";
              }
            ];
          };

        # /clear starts a session with a new session_id: this hands the
        # ending session's plan to the one it starts in the same directory.
        hooks.SessionEnd = [
          {
            matcher = "clear";
            hooks = [
              {
                type = "command";
                command = "${agentPlan} find --hook";
              }
            ];
          }
        ];

        # When Claude Code waits for the operator, an OSC 777 notification,
        # which Ghostty shows on the machine displaying the terminal, over ssh
        # too. Not on Stop, which would notify after every turn.
        hooks.Notification = [
          {
            hooks = [
              {
                type = "command";
                command = ''printf '\033]777;notify;Claude Code;%s\a' "$(${getExe pkgs.jq} -r '.message // "Waiting for input"')"'';
              }
            ];
          }
        ];

        # How many open questions the session's plan has, from its files
        # alone, which the `questions` skill asks the operator.
        # Context and quota too; the rerun drops a window once it resets,
        # and shows when a checkpoint's cache has gone cold.
        statusLine = {
          type = "command";
          command = "${agentPlan} statusline";
          refreshInterval = 60;
        };

        # A hook list set here replaces the file's own on every switch.
        hooks.PreToolUse =
          [
            # Holds every session to the baseline: what is the operator's to do.
            # A reviewer agent's own hook adds its reading rules. Every tool
            # that runs a command or writes a file belongs here: Monitor runs a
            # shell command as Bash does, and ExitWorktree can delete a
            # worktree and its branch. MCP tools are held to the guard's
            # mcp.json: on a server it lists, reads pass, listed writes ask and
            # the rest are refused; every tool on a server it does not list
            # asks. A hook that cannot run, or input it cannot read, denies.
            {
              matcher = "Bash|Monitor|ExitWorktree|Edit|Write|MultiEdit|NotebookEdit|mcp__.*";
              hooks = [
                {
                  type = "command";
                  command = "${getExe' config.wgn.home.llm.agentTools "pr-loop"} guard --baseline";
                  onFailure = "block";
                }
              ];
            }
          ]
          ++ quotaStopHooks;
      };

      # Through a gateway, its settings join whatever these are, its `env`
      # beside the machine's own.
      apply = settings: recursiveUpdate settings gatewaySettings;
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;
      wgn.home.llm.delegate = {
        agents = "${reviewAgents}";
        settings = "${reviewerSettings}";
      };
      # The guard refuses a line naming one of these anywhere but alone.
      wgn.home.llm.guard.excludedCommands = cfg.settings.sandbox.excludedCommands or [];

      assertions = [
        {
          assertion = let
            d = cfg.settings.sandbox.filesystem.denyRead or [];
            h = config.home.homeDirectory;
          in
            all (p: elem p d) ["${h}/.ssh" "${h}/.gnupg" "${h}/.config/gh"];
          message = "wgn.home.claude: the host sandbox no longer denies ~/.ssh, ~/.gnupg or ~/.config/gh";
        }
      ];

      # Claude Code is installed and updated by itself, and owns ~/.claude.json
      # and ~/.claude/settings.json. Only the files it reads without writing
      # are kept here.
      home.file =
        llm.mkSkillFiles pkgs ".claude/skills" config.wgn.home.llm.skills
        // roleAgents
        // {
          ".claude/CLAUDE.md".source = config.wgn.home.llm.instructions;

          # Run only as /questions, so it costs nothing until the operator
          # asks.
          # AskUserQuestion takes one to four questions of two to four
          # options each, and adds "Other" itself.
          ".claude/skills/questions/SKILL.md".text = ''
            ---
            name: questions
            description: Ask the operator the open questions of this session's plan, and record their answers as decisions.
            disable-model-invocation: true
            ---

            # Questions

            1. Run `agent-plan find`; its first result's `plan` is PLAN. With
               none, say this session has no plan, and stop.
            2. Run `agent-plan questions PLAN --all --json`: the questions
               the operator can answer now. With none, say so, and stop.
            3. Ask them with AskUserQuestion, in order, at most four per call.
               Each question's header is its `id`, then ` [#P]` with its
               `priority` if it has one; its text its `question`, then its
               `context` if any. Its options: the `recommendation` first,
               labelled with " (Recommended)" after it, then its
               `alternatives`, then "Drop it", four options at most, dropping
               alternatives from the end to fit.
            4. For "Drop it", run
               `agent-plan decide PLAN ID --drop --answer 'dropped by the operator'`,
               or with their reason if they gave one. For any other answer, run
               `agent-plan decide PLAN ID --answer 'ANSWER'`, the answer on one
               line, without " (Recommended)".
            5. A decision can make a question that waited for it askable:
               run step 2 again, and ask from step 3 the questions it lists
               that were not asked yet, until it lists no new one.
            6. Report each decision in one line: its ID, the question, the
               answer.
          '';
        };

      # Merged rather than linked: a read-only settings.json would make every
      # change made from inside Claude Code last only for that session.
      home.activation.claudeSettings = lib.hm.dag.entryAfter ["writeBoundary"] ''
        settings="$HOME/.claude/settings.json"
        patch=${escapeShellArg (builtins.toJSON cfg.settings)}

        if [ -L "$settings" ]; then
          warnEcho "$settings is a link, so the declared Claude Code settings were not merged into it."
        elif [ ! -e "$settings" ]; then
          if [ -z "''${DRY_RUN:-}" ]; then
            mkdir -p "$(dirname "$settings")"
            ${pkgs.jq}/bin/jq -n --argjson patch "$patch" '$patch' > "$settings"
          fi
        elif merged=$(${pkgs.jq}/bin/jq --argjson patch "$patch" '. * $patch' "$settings"); then
          if [ "$merged" != "$(cat "$settings")" ]; then
            run cp "$settings" "$settings.before-home-manager"
            [ -n "''${DRY_RUN:-}" ] || printf '%s\n' "$merged" > "$settings"
          fi
        else
          warnEcho "$settings is not valid JSON, so the declared Claude Code settings were not merged into it."
        fi
      '';
    }

    # Claude Code runs the review loop.
    {wgn.home.llm.prLoop.enable = mkDefault true;}

    (mkIf config.wgn.home.llm.prLoop.enable {
      home.packages = [reviewLoop];
    })

    # Claude Code's Linux sandbox runs commands through these, found on PATH,
    # and with `failIfUnavailable` a session without them does not start.
    (mkIf ((cfg.settings.sandbox.enabled or false) && pkgs.stdenv.hostPlatform.isLinux) {
      home.packages = [pkgs.bubblewrap pkgs.socat];
    })

    # Matched by name in the sandbox's excluded commands.
    (mkIf (elem "gh-read *" (cfg.settings.sandbox.excludedCommands or [])) {
      home.packages = [ghRead];
    })

    (mkIf cfg.agentPlans.enable (let
      state = "${config.xdg.stateHome}/agent-plans";
      session = "${state}/session";
      out = "${state}/out";
      # The session's own Claude Code config: its login, its trust of the
      # session folder and its settings. /config from the phone writes the
      # user settings, which here are a read-only link into the store, so
      # it changes nothing, and never the operator's ~/.claude.
      claudeDir = "${state}/claude";
      home = config.home.homeDirectory;
      relative = removePrefix "${home}/";
      agentTools = config.wgn.home.llm.agentTools;
      # The session's Bash runs plan-dash by name, and plan-dash runs git
      # and gh; Claude Code installs itself under ~/.local/bin.
      path = concatStringsSep ":" [
        (makeBinPath [agentTools pkgs.git pkgs.openssh pkgs.gh pkgs.coreutils])
        "${config.home.profileDirectory}/bin"
        "${home}/.local/bin"
        "/usr/local/bin:/usr/bin:/bin"
      ];
    in {
      assertions = [
        {
          assertion = pkgs.stdenv.hostPlatform.isLinux;
          message = "wgn.home.claude.agentPlans needs Linux.";
        }
        {
          assertion = config.wgn.home.llm.gateway == "none";
          message = "wgn.home.claude.agentPlans runs claude remote-control, which Claude Code disables while it reaches its models through a gateway (wgn.home.llm.gateway).";
        }
      ];

      # The session's working directory; its settings live in claudeDir.
      home.activation.agentPlansSession = lib.hm.dag.entryAfter ["writeBoundary"] ''
        run mkdir -p ${escapeShellArg session}
      '';

      home.file = {
        # The session's user settings, read-only. dontAsk denies whatever
        # they do not allow. The allow list names the skill's tools, and the
        # baseline hook holds their commands; the quota stop holds it as it
        # does every session; with onFailure block a hook that cannot run
        # denies. Its SessionStart output becomes the
        # session's context after a start, /clear or /compact.
        "${relative claudeDir}/settings.json".text = builtins.toJSON {
          permissions = {
            allow = [
              "Bash(plan-dash export)"
              "Bash(plan-dash page)"
              "Bash(plan-dash page published)"
              "Bash(plan-dash space)"
              "Artifact"
              "ToolSearch"
              "Monitor"
              "ArtifactData"
              "Agent"
              "Skill"
              "PushNotification"
            ];
            deny =
              concatMap (p: ["Read(${rulePath p}*)" "Read(${rulePath p}*/**)"]) config.wgn.home.llm.guard.credentialPaths
              ++ ["Read(${rulePath plans.repository.key})"];
          };
          syncClaudeAiSkills = false;
          syncClaudeAiPlugins = false;
          hooks = {
            PreToolUse =
              [
                {
                  matcher = "Bash|Monitor|ExitWorktree|Edit|Write|MultiEdit|NotebookEdit|mcp__.*";
                  hooks = [
                    {
                      type = "command";
                      command = "${getExe' agentTools "pr-loop"} guard --baseline";
                      onFailure = "block";
                    }
                  ];
                }
              ]
              ++ quotaStopHooks;
            SessionStart = [
              {
                matcher = "startup|clear|compact";
                hooks = [
                  {
                    type = "command";
                    command = "${getExe' agentTools "plan-dash"} prime";
                  }
                ];
              }
            ];
          };
        };

        # In the session's own config alone, so no other session loads it.
        "${relative claudeDir}/skills/agent-plans/SKILL.md".text =
          replaceStrings ["@out@"] [out] (builtins.readFile ./agent-plans/SKILL.md);
      };

      systemd.user.services.agent-plans = {
        Unit = {
          Description = "The Agent plans page's Remote Control session";
          ConditionPathExists = "!${state}/off";
        };
        Service = {
          # --spawn session ends when its session does, with status 0, so
          # only Restart=always brings it back. Without a terminal it
          # redraws its status line every second, which would fill the
          # journal.
          ExecStart = concatStringsSep " " [
            "${home}/.local/bin/claude remote-control --spawn session"
            "--working-directory ${session}"
            "--permission-mode dontAsk"
            "--remote-control-session-name-prefix agent-plans"
          ];
          WorkingDirectory = session;
          Environment = ["CLAUDE_CONFIG_DIR=${claudeDir}" "PATH=${path}"];
          Restart = "always";
          RestartSec = "30s";
          StandardOutput = "null";
        };
        Install.WantedBy = ["default.target"];
      };
    }))

    # User-scoped servers live in ~/.claude.json, which Claude Code rewrites
    # constantly, so these entries are merged in on every switch rather than
    # owned. headersHelper reads the token when Claude Code connects.
    (mkIf mcpProxy.enable {
      home.activation.claudeMcpServers = lib.hm.dag.entryAfter ["writeBoundary" "mcpProxyToken"] ''
        state="$HOME/.claude.json"
        servers=${escapeShellArg (builtins.toJSON (mapAttrs (_: path: {
            type = "http";
            url = mcpProxy.url + path;
            inherit (mcpProxy) headersHelper;
          })
          mcpProxy.endpoints))}

        if [ ! -e "$state" ]; then
          verboseEcho "$state does not exist yet; the MCP proxy is registered at the next switch after Claude Code first runs."
        elif merged=$(${pkgs.jq}/bin/jq --argjson servers "$servers" '.mcpServers = ((.mcpServers // {}) + $servers)' "$state"); then
          if [ "$merged" != "$(cat "$state")" ] && [ -z "''${DRY_RUN:-}" ]; then
            printf '%s\n' "$merged" > "$state"
          fi
        else
          warnEcho "$state is not valid JSON, so the MCP proxy was not registered with Claude Code."
        fi
      '';
    })
  ]);
}
