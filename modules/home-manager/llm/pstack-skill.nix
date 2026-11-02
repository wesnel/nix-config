# A pstack skill linked as published, with pstack's MIT licence beside it:
# the licence requires its notice in every copy. One whose SKILL.md keeps
# Claude Code from invoking it unasked, as disables-model-invocation.nix
# reads it, gets grill's agents/openai.yaml, which does the same in Codex;
# one whose frontmatter that read cannot decide fails to build. `build`
# runs in the copy first, so it can change that SKILL.md. The `pstack`
# attribute marks it for checks.pstack-skills, which also builds one from
# each of its SKILL.md cases the read decides.
{
  cursor-plugins,
  pkgs,
}: name: build:
pkgs.runCommandLocal name {
  nativeBuildInputs = [pkgs.makeWrapper];
  passthru.pstack = true;
} ''
  cp -R ${cursor-plugins}/pstack/skills/${name} $out
  chmod -R u+w $out
  cp ${cursor-plugins}/pstack/LICENSE $out/LICENSE
  cd $out
  ${build}
  status=0
  ${pkgs.callPackage ./disables-model-invocation.nix {}} SKILL.md || status=$?
  case $status in
    0)
      mkdir -p agents
      cp ${./skills/grill/agents/openai.yaml} agents/openai.yaml
      ;;
    1) ;;
    *) exit "$status" ;;
  esac
''
