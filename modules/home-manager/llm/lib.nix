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
    sandbox = given.sandbox or false;
  };

  # Names of the skills that need nothing but the workspace and the tools the
  # sandbox itself provides.
  sandboxSkillNames = skills:
    lib.attrNames (lib.filterAttrs (_: skill: (normalise skill).sandbox) skills);

  # A source that is not itself a SKILL.md is the skill's whole directory:
  # SKILL.md with the scripts and Emacs Lisp it loads from beside itself.
  isDirectory = source: !(lib.hasSuffix "/SKILL.md" (toString source));

  # Renders a skill set as home.file entries under dir. The agents all
  # discover skills the same way, as <dir>/<name>/SKILL.md, and they read
  # that directory without writing to it, which is what makes it the one
  # part of their configuration worth keeping here.
  mkSkillFiles = dir: skills:
    lib.mapAttrs' (
      name: given: let
        skill = normalise given;
      in
        if skill.source != null && isDirectory skill.source
        then lib.nameValuePair "${dir}/${name}" {source = skill.source;}
        else
          lib.nameValuePair "${dir}/${name}/SKILL.md" (
            if skill.source != null
            then {source = skill.source;}
            else {text = skill.text;}
          )
    )
    skills;
}
