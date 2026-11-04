package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
)

// This catalog also supplies Codex's routine plan-tool approvals.
//
//go:embed plan-tools.json
var routinePlanToolsJSON []byte

func init() {
	var names []string
	if err := json.Unmarshal(routinePlanToolsJSON, &names); err != nil {
		panic(err)
	}
	available := planTools()
	seen := map[string]bool{}
	for _, name := range names {
		i := slices.IndexFunc(available, func(t mcpTool) bool { return t.name == name })
		if i < 0 || seen[name] {
			panic("invalid routine plan tool: " + name)
		}
		seen[name] = true
		mcpTools = append(mcpTools, available[i])
	}
}

// planTool runs only the sibling shipped with this server, even when the
// server was started through a profile symlink or PATH holds another CLI.
func planTool(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	if err := git.CheckLocation(); err != nil {
		return cli.Report("agent-plan", nil, err, 0, stdout, stderr)
	}
	if err := git.CheckConfig(""); err != nil {
		return cli.Report("agent-plan", nil, err, 0, stdout, stderr)
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(stderr, "agent-plan executable:", err)
		return 1
	}
	cmd := exec.CommandContext(ctx, filepath.Join(filepath.Dir(exe), "agent-plan"), argv...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "agent-plan:", err)
		return 1
	}
	return 0
}

func planTools() []mcpTool {
	p := func(name string, kind paramKind, required bool, desc string) mcpParam {
		return mcpParam{name: name, kind: kind, required: required, desc: desc}
	}
	plan := p("plan", text, true, "the plan file or name in the configured plans directory")
	part := p("part", text, false, "the plan part, as P1")
	title := p("title", literal, true, "the title")
	content := p("text", literal, false, "inline plan text; no Org headings or drawers")
	after := p("after", texts, false, "the item IDs this item waits for")
	item := p("item", text, true, "the item ID")
	role := p("role", text, false, "the role recording the event or decision")
	flag := func(name string) mcpParam {
		return p(name, text, false, "the --"+strings.ReplaceAll(name, "_", "-")+" value")
	}
	prose := func(name string) mcpParam { return p(name, literal, false, "inline "+name) }
	list := func(name string) mcpParam { return p(name, texts, false, "the repeated --"+name+" values") }
	boolFlag := func(name, desc string) mcpParam { return p(name, toggle, false, desc) }
	enum := func(name string, required bool, values ...string) mcpParam {
		return mcpParam{name: name, kind: text, required: required, enum: values, desc: strings.Join(values, ", ")}
	}
	makeTool := func(command string, positional []string, params ...mcpParam) mcpTool {
		return mcpTool{
			name:   "agent_plan_" + strings.NewReplacer("-", "_", " ", "_").Replace(command),
			desc:   "agent-plan " + command + ": use the configured plans directory and the CLI's validation; inline text only.",
			params: params,
			argv: func(a mcpArgs) []string {
				argv := strings.Fields(command)
				for _, param := range params {
					if slices.Contains(positional, param.name) {
						continue
					}
					name := strings.ReplaceAll(param.name, "_", "-")
					if param.name == "target_worktree" || param.name == "target_worktrees" {
						name = "worktree"
					}
					switch v := a[param.name].(type) {
					case string:
						argv = append(argv, "--"+name+"="+v)
					case []string:
						for _, value := range v {
							argv = append(argv, "--"+name+"="+value)
						}
					case bool:
						if v {
							argv = append(argv, "--"+name)
						}
					}
				}
				for _, name := range positional {
					switch v := a[name].(type) {
					case string:
						argv = append(argv, v)
					case []string:
						argv = append(argv, v...)
					}
				}
				return argv
			}, run: planTool,
		}
	}
	return []mcpTool{
		makeTool("new", nil, title, flag("slug"), list("repo"), list("branch"),
			p("target_worktrees", texts, false, "plan worktrees; defaults to the execution worktree"), list("session"),
			flag("harness"), flag("template"), flag("workstream"), flag("parent"), part),
		makeTool("log", []string{"plan"}, plan, item, p("kind", text, true, "event kind; tool-owned kinds are refused"),
			prose("summary"), role, flag("agent"), flag("branch"), prose("commit"), flag("revision"), prose("evidence"),
			flag("review"), flag("outcome"), p("command", literal, false, "hand-back command stored as text, never executed"), flag("dir")),
		makeTool("set", []string{"plan", "property", "values"}, plan,
			enum("property", true, "WORKTREES", "BRANCHES", "PULL_REQUESTS", "STATUS", "SKILLS", "REPOSITORIES", "SESSIONS", "HARNESS"),
			p("values", texts, true, "the property's values, or an empty array to clear it"), part),
		makeTool("unfile", []string{"plan", "property", "value"}, plan,
			enum("property", true, "WORKTREES", "BRANCHES", "PULL_REQUESTS"), p("value", text, true, "the value to remove"),
			p("reason", literal, true, "why the value belongs to another plan"), part),
		makeTool("ask", []string{"plan"}, plan, p("question", literal, true, "the question"),
			p("recommendation", literal, true, "the recommended answer"), prose("context"), list("alternative"), after,
			enum("priority", false, "A", "B", "C"), part, flag("kind"), prose("answer"), role),
		makeTool("decide", []string{"plan", "question"}, plan, p("question", text, true, "the question ID"),
			p("answer", literal, true, "the answer or reason to drop it"), boolFlag("drop", "drop the question"), flag("supersedes"), role),
		makeTool("followup", []string{"plan"}, plan, title, p("found_in", text, true, "the item where the work was found"), prose("context"), part),
		makeTool("step", []string{"plan"}, plan, title, content, after, part),
		makeTool("part", []string{"plan"}, plan, title, content, after, flag("repo"), flag("branch"),
			p("target_worktree", text, false, "the part's worktree")),
		makeTool("edit", []string{"plan", "item"}, plan, item, prose("title"), content),
		makeTool("after", []string{"plan", "item", "after"}, plan, item, after, boolFlag("clear", "clear dependencies")),
		makeTool("section", []string{"plan", "section"}, plan, enum("section", true, "Goal", "Verification"),
			p("text", literal, true, "inline section text; Goal must be nonempty"), part),
		makeTool("reference", []string{"plan", "url"}, plan, p("url", text, true, "the reference URL"), prose("note")),
		makeTool("link", []string{"plan", "other"}, plan, p("other", text, true, "the related plan file or name"), p("why", literal, true, "why the plans are related")),
		makeTool("postpone", []string{"plan", "item"}, plan, item, flag("until"), boolFlag("clear", "clear the postponement")),
		makeTool("sign-id", nil, plan, list("branch"), flag("session"), flag("base")),
		makeTool("workstream new", nil, title, prose("summary")),
		makeTool("workstream list", nil, boolFlag("json", "return JSON")),
		makeTool("find", nil, flag("session"), flag("branch"), p("target_worktree", text, false, "match this worktree")),
		makeTool("status", []string{"plan"}, plan, enum("format", false, "org", "json")),
	}
}
