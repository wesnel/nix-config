{lib}: let
  # A skill that reaches for something only this machine has is worse than
  # absent inside a sandbox: the agent is told it exists and spends its turn
  # calling it. The marker travels with the skill so whoever mounts the
  # directory can tell, rather than keeping a list that drifts from it.
  hostOnlyMarker = ".host-only";

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

  skillEntries = dir: name: given: let
    skill = normalise given;
  in
    [
      (lib.nameValuePair "${dir}/${name}/SKILL.md" (
        if skill.source != null
        then {source = skill.source;}
        else {text = skill.text;}
      ))
    ]
    ++ lib.optional (!skill.sandbox) (
      lib.nameValuePair "${dir}/${name}/${hostOnlyMarker}" {
        text = ''
          This skill needs something only the host has, so it is left out of
          the sandbox rather than offered there and failing when called.
        '';
      }
    );
in {
  inherit hostOnlyMarker;

  # Renders a skill set as home.file entries under dir. The agents all
  # discover skills the same way, as <dir>/<name>/SKILL.md, and they read
  # that directory without writing to it, which is what makes it the one
  # part of their configuration worth keeping here.
  mkSkillFiles = dir: skills:
    lib.listToAttrs (
      lib.concatLists (lib.mapAttrsToList (skillEntries dir) skills)
    );
}
