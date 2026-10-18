# The agents the coordinate-subagents skill starts by role, beside
# `reviewer`, which each harness's module defines from here with
# frontmatter of its own: its tools, model and how it loads the skills
# named here. agents.json names which agents each harness defines, and a
# reviewer there is given no editing tools.
{
  lib,
  agentTools,
}:
with lib; let
  # What a body names as @key@ and each harness says differently, so that
  # every agent is told only what is true where it runs.
  harnessText = {
    claude = {
      editingTools = "You have no editing tools.";
      projectAgents = ''
        A repository's `.claude/agents` can replace the operator's agents, and
        where `--add-dir` brought it into the session the guard cannot see it'';
    };
    # The ECA module disables a reviewer's write_file, edit_file and
    # move_file and leaves it its MCP tools, which the guard holds to the
    # reads mcp.json lists. An agent inherits `code`, settings included.
    eca = {
      editingTools = "You have no file-editing tools, and the guard lets your MCP tools only read.";
      projectAgents = ''
        A repository's `.eca/agents`, or an `agent` entry in its
        `.eca/config.json`, replaces the operator's agent of the same name in a
        session with that repository among its workspace folders; its
        `agent.code` entry changes the approvals, tools and model you inherit
        from `code`; and the guard looks for none of them'';
    };
  };

  # Every key of harnessText.<harness> a body names, and nothing else between
  # @ signs, which `prompt` refuses.
  fill = harness: name: body: let
    text = harnessText.${harness};
    filled = replaceStrings (map (k: "@${k}@") (attrNames text)) (attrValues text) body;
  in
    assert assertMsg (length (builtins.split "@[A-Za-z]+@" filled) == 1)
    "llm/agents.nix: ${name}'s body names text ${harness} does not give"; filled;

  # A description goes into each harness's frontmatter as a plain YAML
  # scalar, which a colon followed by a space, or a # after one, would cut
  # short.
  plainYAML = mapAttrs (name: d:
    assert assertMsg (!(hasInfix ": " d.description || hasInfix " #" d.description))
    "llm/agents.nix: ${name}'s description has \": \" or \" #\", which end a plain YAML scalar"; d);
in rec {
  definitions = plainYAML {
    builder = {
      description = "Builds one part of a plan, one branch in the worktree its task names, committed unsigned and verified.";
      skills = ["signing-pass" "write-tests" "review-change" "plan-work"];
      body = builtins.readFile ./agents/builder.md;
    };
    adversarial-reviewer = {
      description = "Tries to break one branch before it is signed, reproducing each finding and rating it CONFIRMED or PLAUSIBLE. Only reads, and changes only throwaway copies.";
      skills = ["write-tests"];
      body = builtins.readFile ./agents/adversarial-reviewer.md;
    };
    design-reviewer = {
      description = "Reviews a plan before it is built, or the work across a plan's branches once built, against the design skill. Only reads.";
      skills = ["design" "review-change"];
      body = builtins.readFile ./agents/design-reviewer.md;
    };
    security-reviewer = {
      description = "Reviews a change for security (untrusted input, secrets, confinement, injection), and answers git fresh-review --focus security prompts. Only reads.";
      skills = [];
      body = builtins.readFile ./agents/security-reviewer.md;
      contract = agentTools.securityReviewerPrompt;
    };
  };

  # The system prompt of the agent `name` for `harness`, a key of
  # harnessText: its body, the rules every one of them shares, then its
  # contract, if any.
  prompt = harness: name: let
    d = definitions.${name};
  in ''
    ${fill harness name d.body}
    ${builtins.readFile ./agents/subagent.md}
    ${d.contract or ""}
  '';
}
