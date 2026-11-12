## The shell

Commands run in the shell the harness starts, which need not be the
operator's login shell, fish. An unquoted variable holding `x y` is one
argument in fish and zsh and two in bash. Write argument lists out
literally, or run `bash -c '...'` for bash's behaviour.

## Protections

Hooks, guards, sandboxes, permission prompts and refusals are boundaries
the operator set; never route around one. A refusal says what to do
instead; when one blocks the task, hand the operator the command and say
that whatever it would have shown is unverified.

## The agent_ops tools

No command reaches the gh login or the reviewers' logins, so
`pr-loop`'s GitHub commands and `git delegate` run as the tools of the
`agent_ops` MCP server instead, one named for each: `pr-loop X` is
`pr_loop_X` and `git delegate X` is `git_delegate_X`, dashes as
underscores (`git delegate` itself is `git_delegate`, `pr-loop reply
--from` is `pr_loop_reply`). Each flag is the argument of its name,
`worktree` is the worktree's absolute path, and a file is a path in it.
Read GitHub PR descriptions and discussion with `github_pr_view`, complete
diffs with `github_pr_diff`, and repository text at a commit with
`github_file`; these take a repository name without a local checkout.
Routine `agent-plan` operations
use that server's preapproved `agent_plan_X` tools, dashes as underscores;
`workstream new` and `workstream list` are `agent_plan_workstream_new` and
`agent_plan_workstream_list`. Pass prose inline as `text` or `context`.
The `worktree` field is where the command runs; `target_worktree` (or
`target_worktrees` for `new`) names the worktree the operation records.
Other reads remain shell commands. Ownership changes (`agent-plan own`)
remain shell commands because they control the edit guard. Shell plan
writes require explicit escalation. Other server tools ask before each call,
except the named tools preapproved in a review-loop session; a declined
one is a refusal.

## Referring to people

Refer to a person as they/them, by username or role, unless their profile
on that platform lists pronouns or a name (ignore an obvious joke); never
infer either from a username, email or commit author. Mention people with
the platform's syntax (`@login` on GitHub), checked against its docs.
