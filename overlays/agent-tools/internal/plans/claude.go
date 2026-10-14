package plans

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
)

// claudeHook is the part of a Claude Code hook event these read.
type claudeHook struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Plan         string `json:"plan"`
		PlanFilePath string `json:"planFilePath"`
	} `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

func hookOutput(event, context string) ([]byte, error) {
	return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": context}})
}

// checkout is the top level and branch of the worktree containing dir,
// asked of that directory alone.
func checkout(dir string) (top, branch string) {
	if dir == "" {
		return "", ""
	}
	top, err := git.Run("-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ""
	}
	branch, _ = git.Run("-C", dir, "branch", "--show-current")
	return top, branch
}

// ImportClaude records the plan a PostToolUse hook on Claude Code's
// ExitPlanMode hands over, verbatim, in the session's plan, which it
// creates if the session has none. It returns the hook's output, or
// nothing when the event carries no plan.
func (c Config) ImportClaude(hook []byte, now time.Time) ([]byte, error) {
	var in claudeHook
	if err := json.Unmarshal(hook, &in); err != nil {
		return nil, err
	}
	var response struct {
		Plan     string `json:"plan"`
		FilePath string `json:"filePath"`
	}
	json.Unmarshal(in.ToolResponse, &response)
	markdown, source := response.Plan, response.FilePath
	if markdown == "" {
		markdown = in.ToolInput.Plan
	}
	if source == "" {
		source = in.ToolInput.PlanFilePath
	}
	if strings.TrimSpace(markdown) == "" {
		return nil, nil
	}
	if err := checkLine("source", source); err != nil {
		source = ""
	}
	file := ""
	if in.SessionID != "" {
		found, err := c.Find(FindOptions{Session: "claude:" + in.SessionID})
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			if f.Part == "" {
				file = f.File
				break
			}
		}
	}
	if file == "" {
		o := NewOptions{Title: planTitle(markdown), Slug: slug(planTitle(markdown)), Harness: "claude"}
		if in.SessionID != "" && checkWords("session", []string{in.SessionID}) == nil {
			o.Sessions = []string{"claude:" + in.SessionID}
		}
		if top, branch := checkout(in.Cwd); top != "" && checkWords("worktree", []string{top}) == nil {
			o.Worktrees = []string{top}
			if branch != "" && checkWords("branch", []string{branch}) == nil {
				o.Branches = []string{branch}
			}
		}
		made, err := c.New(o, now)
		if err != nil {
			return nil, err
		}
		file = made.File
	}
	var b strings.Builder
	b.WriteString("* Approved plan\n:PROPERTIES:\n")
	if source != "" {
		b.WriteString(":SOURCE: " + source + "\n")
	}
	b.WriteString(":END:\n#+begin_src markdown\n" + escapeOrg(strings.TrimRight(markdown, "\n")) + "\n#+end_src\n")
	if err := c.appendFile(file, b.String()); err != nil {
		return nil, err
	}
	return hookOutput("PostToolUse", fmt.Sprintf("The approved plan is recorded verbatim in %s, under \"Approved plan\". "+
		"Before the first edit, structure it into that file's Goal, Steps and Questions, as the plan-work skill says, "+
		"and log each step's progress with `agent-plan log`.", file))
}

// appendFile adds text at the end of a plan's file, after a line break if
// the file lacks one.
func (c Config) appendFile(path, text string) error {
	p, err := c.Open(path)
	if err != nil {
		return err
	}
	return p.withLock(func(l *lockedPlan) error {
		return l.edit(path, func(lines []string) ([]string, error) {
			existing := strings.Join(lines, "\n")
			if existing != "" && !strings.HasSuffix(existing, "\n") {
				existing += "\n"
			}
			return strings.Split(existing+text, "\n"), nil
		})
	})
}

// planTitle is a Markdown plan's first heading, or its first line.
func planTitle(markdown string) string {
	first := ""
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if title, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(title)
		}
		if first == "" {
			first = strings.TrimLeft(line, "#*- ")
		}
	}
	if first == "" {
		return "Approved plan"
	}
	if len(first) > 60 {
		first = first[:60]
	}
	return first
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(title string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		return "plan"
	}
	return s
}

// SessionHook names the plans of the session Claude Code's SessionStart
// hook describes, matched by session or by the worktree it runs in, as
// the hook's output; nothing when there are none.
func (c Config) SessionHook(hook []byte) ([]byte, error) {
	var in claudeHook
	if err := json.Unmarshal(hook, &in); err != nil {
		return nil, err
	}
	o := FindOptions{}
	if in.SessionID != "" {
		o.Session = "claude:" + in.SessionID
	}
	o.Worktree, _ = checkout(in.Cwd)
	if o.Session == "" && o.Worktree == "" {
		return nil, nil
	}
	found, err := c.Find(o)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	var lines []string
	for _, f := range found {
		lines = append(lines, fmt.Sprintf("- %s (%s, status %s, matched by %s)", f.File, f.Title, f.State, strings.Join(f.Matched, " and ")))
	}
	return hookOutput("SessionStart", "Plans for this session:\n"+strings.Join(lines, "\n")+
		"\nRead yours, and `agent-plan status PLAN` for its progress, before going on; keep it true as the plan-work skill says.")
}
