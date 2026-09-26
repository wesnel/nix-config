{lib}: {
  # Renders a skill set as home.file entries under dir. The agents all
  # discover skills the same way, as <dir>/<name>/SKILL.md, and they read
  # that directory without writing to it, which is what makes it the one
  # part of their configuration worth keeping here.
  mkSkillFiles = dir: skills:
    lib.mapAttrs' (
      name: content:
        lib.nameValuePair "${dir}/${name}/SKILL.md" (
          if lib.isPath content
          then {source = content;}
          else {text = content;}
        )
    )
    skills;
}
