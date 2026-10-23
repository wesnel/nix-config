# The principle skills: each directory under ./skills/principles is one,
# named after it.
builtins.mapAttrs (name: _: ./skills/principles + "/${name}/SKILL.md")
(builtins.readDir ./skills/principles)
