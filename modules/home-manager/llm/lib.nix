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
}
