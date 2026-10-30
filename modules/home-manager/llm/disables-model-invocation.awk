# Exits 0 when the SKILL.md it reads keeps Claude Code from invoking the
# skill unasked: a line that is exactly `disable-model-invocation: true`.
# pstackSkill reads it to give the skill Codex's policy, and
# checks.pstack-skills to check that it did.
$0 == "disable-model-invocation: true" { found = 1 }
END { exit !found }
