{lib}: rec {
  # Callers add skills of their own straight to the set, which never passes
  # through the option's type, so the shorthands are accepted here too.
  normalise = skill: let
    given =
      if lib.isPath skill
      then {source = skill;}
      else if lib.isString skill
      then {text = skill;}
      else skill;
  in {
    source = given.source or null;
    text = given.text or null;
  };

  # A source that is not itself a SKILL.md is the skill's whole directory:
  # SKILL.md with the scripts and Emacs Lisp it loads from beside itself.
  isDirectory = source: !(lib.hasSuffix "/SKILL.md" (toString source));

  # Renders a skill set as home.file entries under dir. The agents all
  # discover skills the same way, as <dir>/<name>/SKILL.md, and they read
  # that directory without writing to it, which is what makes it the one
  # part of their configuration worth keeping here.
  #
  # Every skill is linked as a directory, never as a bare SKILL.md symlink:
  # Codex skips a SKILL.md that is itself a symlink. A single-file skill is
  # therefore wrapped in a store directory holding a real copy of its file.
  mkSkillFiles = pkgs: dir: skills:
    lib.mapAttrs' (
      name: given: let
        skill = normalise given;
        wrapped =
          if skill.source != null
          then
            pkgs.runCommand "skill-${name}" {} ''
              mkdir $out
              cp ${skill.source} $out/SKILL.md
            ''
          else pkgs.writeTextDir "SKILL.md" skill.text;
      in
        lib.nameValuePair "${dir}/${name}" {
          source =
            if skill.source != null && isDirectory skill.source
            then skill.source
            else wrapped;
        }
    )
    skills;

  # Each focused review, a route of agent-tools' routes.Known, has a
  # reviewer of its own, ROUTE-reviewer, whose contract is that focus's.
  focusReviewers = pkgs: map (route: "${route}-reviewer") (lib.attrNames pkgs.agent-tools.focusPrompts);

  # The role agents, from the plain agent files in ../claude/agents, which
  # both harnesses start as sub-agents: Claude Code as agent files, Codex as
  # roles. In each file, guard (pr-loop) replaces @guard@. A file is YAML
  # frontmatter between `---` lines, then its prompt; a description is plain
  # YAML, with no colon followed by a space. Each role agent is:
  # - field KEY: the frontmatter's `KEY: VALUE` line's value, or null;
  # - frontmatter: the frontmatter's lines;
  # - skills: the `skills:` list, one `  - NAME` line per skill;
  # - text: the whole file, and prompt: what follows the frontmatter, each
  #   ending with the rules every sub-agent shares, then its contract, if any.
  agentFiles = pkgs: guard: let
    contracts = lib.mapAttrs' (route: lib.nameValuePair "${route}-reviewer") pkgs.agent-tools.focusPrompts;
    shared = builtins.readFile ../claude/agents/subagent.md;
    parse = name: let
      raw = builtins.readFile (../claude/agents + "/${name}.md");
      text = lib.replaceStrings ["@guard@"] [guard] raw;
      pieces = lib.splitString "\n---\n" (lib.removePrefix "---\n" text);
      frontmatter = lib.splitString "\n" (lib.head pieces);
      field = key: let
        lines = lib.filter (lib.hasPrefix "${key}: ") frontmatter;
      in
        if lines == []
        then null
        else lib.removePrefix "${key}: " (lib.head lines);
      # Read from the file as written: a name taken from `text` carries
      # pr-loop's store path, which an attribute name cannot.
      skills = let
        lines = lib.splitString "\n" (lib.head (lib.splitString "\n---\n" (lib.removePrefix "---\n" raw)));
        start = lib.lists.findFirstIndex (line: line == "skills:") null lines;
        after =
          if start == null
          then []
          else lib.drop (start + 1) lines;
      in
        map (lib.removePrefix "  - ") (lib.take (lib.lists.findFirstIndex (line: !lib.hasPrefix "  - " line) (lib.length after) after) after);
      ending = "\n" + shared + (contracts.${name} or "");
    in
      assert lib.assertMsg (lib.hasPrefix "---\n" text && lib.length pieces > 1) "llm: ../claude/agents/${name}.md has no frontmatter";
      assert lib.assertMsg (field "name" == name) "llm: ../claude/agents/${name}.md is not named ${name}"; {
        inherit field frontmatter skills;
        text = text + ending;
        prompt = lib.concatStringsSep "\n---\n" (lib.tail pieces) + ending;
      };
  in
    lib.genAttrs (["builder" "adversarial-reviewer" "design-reviewer" "review-guide" "diagrammer"] ++ focusReviewers pkgs) parse;
}
