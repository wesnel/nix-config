{lib}: {
  enabled,
  watcher,
  plansDir,
  devboxEnabled,
  devboxService,
  claudeHooks,
  codexHooks,
  sharedSkill,
  claudeExclusions,
  claudeAllow,
  claudeWrites,
  codexFilesystem,
  home,
  mirrorState,
}: let
  mirrorCommands = ["plan-dash claim *" "plan-dash release *" "plan-dash snapshot *"];
  hasStateWrite = paths:
    builtins.any
    (path: let
      expanded =
        if lib.hasPrefix "~/" path
        then home + "/" + lib.removePrefix "~/" path
        else path;
    in
      expanded == mirrorState || lib.hasPrefix (expanded + "/") mirrorState)
    paths;
  command = suffix: hooks: event:
    builtins.any
    (group:
      builtins.any
      (hook: lib.hasSuffix suffix (hook.command or ""))
      (group.hooks or []))
    (hooks.${event} or []);
  commandForTool = suffix: hooks: event: tool:
    builtins.any
    (group:
      builtins.any
      (hook: (hook.type or "") == "command" && lib.hasSuffix suffix (hook.command or ""))
      (group.hooks or [])
      && (let
        pattern = group.matcher or ".*";
        regex =
          if builtins.match "[A-Za-z0-9_|-]+" pattern != null
          then pattern
          else ".*(" + pattern + ").*";
      in
        builtins.match regex tool != null))
    (hooks.${event} or []);
  publications = [
    "Artifact"
    "mcp__codex_apps__chatgpt_space__create_page"
    "mcp__codex_apps__chatgpt_space__create_page_visualization"
  ];
  otherTools = [
    "ArtifactData"
    "Bash"
    "mcp__codex_apps__chatgpt_space__read_page"
    "mcp__codex_apps__chatgpt_space__update_page"
    "mcp__codex_apps__chatgpt_space__create_page_extra"
    "mcp__codex_apps__chatgpt_space__create_page_visualization_extra"
    "mcp__other__create_page"
  ];
  guardedPublication = hooks:
    builtins.all (commandForTool "pr-loop guard --baseline" hooks "PreToolUse") publications
    && builtins.all (commandForTool "plan-dash record" hooks "PostToolUse") publications
    && builtins.all (tool: !(commandForTool "plan-dash record" hooks "PostToolUse" tool)) otherTools;
  config =
    if watcher == null
    then {}
    else watcher.config;
  args = config.ProgramArguments or [];
in
  enabled
  && watcher != null
  && watcher.enable
  && (config.WatchPaths or []) == [plansDir]
  && (config.StartInterval or null) == 300
  && builtins.length args == 2
  && lib.hasSuffix "/bin/plan-dash" (builtins.head args)
  && builtins.tail args == ["refresh"]
  && lib.hasInfix "/.local/bin:" (config.EnvironmentVariables.PATH or "")
  && !devboxEnabled
  && !devboxService
  && sharedSkill
  && builtins.sort builtins.lessThan (builtins.filter (lib.hasPrefix "plan-dash") claudeExclusions) == mirrorCommands
  && builtins.sort builtins.lessThan (builtins.filter (lib.hasPrefix "Bash(plan-dash") claudeAllow) == map (c: "Bash(${c})") mirrorCommands
  && !(hasStateWrite claudeWrites)
  && !(hasStateWrite (builtins.attrNames (lib.filterAttrs (_: access: access == "write") codexFilesystem)))
  && builtins.all (command "plan-dash remind" claudeHooks) ["SessionStart" "UserPromptSubmit"]
  && builtins.all (command "plan-dash remind" codexHooks) ["SessionStart" "UserPromptSubmit"]
  && guardedPublication claudeHooks
  && guardedPublication codexHooks
