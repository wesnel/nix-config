# The principle skills: each directory under ./skills/principles is one,
# named after it. flake.nix's skill-names check holds each to one home.
builtins.mapAttrs (name: _: ./skills/principles + "/${name}/SKILL.md")
(builtins.readDir ./skills/principles)
