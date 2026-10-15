package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Urgent is the share of a window, in percent, at which the agents are
// asked again.
const Urgent = 90

// modelProviders are the providers whose quota an ECA model draws on, by
// the provider part of its full_model.
var modelProviders = map[string]string{"anthropic": "claude", "openai": "codex"}

// tierHints say what one tier down is for each provider's sub-agents.
var tierHints = map[string]string{
	"claude": " (Opus to Sonnet, Sonnet to Haiku)",
	"codex":  " (a smaller model, through spawn_agent's model)",
}

// Remind is the usage part of `agent-plan remind`: on a main thread's
// prompt, it asks for Wesley's choice of tiers when a window of the
// session's provider has crossed Warn or Urgent since the last ask on this
// host, or returns "". Codex's turn records the reading of its rollout
// first, since Codex has no status line that does.
//
// The prompts are Claude Code's and Codex's UserPromptSubmit, Codex's with
// a turn_id and, for a sub-agent's turn, an agent_id; and ECA's
// preRequest, whose provider is that of its full_model, a sub-agent's
// chat naming a parent_chat_id.
func Remind(s State, input []byte, now time.Time) string {
	var in struct {
		Event        string `json:"hook_event_name"`
		Type         string `json:"hook_type"`
		AgentID      string `json:"agent_id"`
		TurnID       string `json:"turn_id"`
		Transcript   string `json:"transcript_path"`
		ParentChatID string `json:"parent_chat_id"`
		FullModel    string `json:"full_model"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var provider string
	switch {
	case in.Event == "UserPromptSubmit" && in.AgentID == "" && in.TurnID != "":
		provider = "codex"
		if r, ok := FromCodexRollout(in.Transcript, now); ok {
			s.Record(r)
		}
	case in.Event == "UserPromptSubmit" && in.AgentID == "":
		provider = "claude"
	case in.Type == "preRequest" && in.ParentChatID == "":
		name, _, _ := strings.Cut(in.FullModel, "/")
		provider = modelProviders[name]
	}
	if provider == "" {
		return ""
	}
	return s.ask(provider, now)
}

// ask is the ask for provider when a window has crossed a threshold that
// no session on this host has been asked about, marking it asked.
func (s State) ask(provider string, now time.Time) string {
	s.clearMarks(now)
	r, ok := s.Reading(provider, now)
	if !ok {
		return ""
	}
	crossed := false
	for _, w := range r.Windows {
		level := 0
		switch {
		case w.Used >= Urgent:
			level = Urgent
		case w.Used >= Warn:
			level = Warn
		default:
			continue
		}
		if s.mark(fmt.Sprintf("%s-%s-%d-%d", provider, w.Name, w.ResetsAt, level)) {
			crossed = true
		}
	}
	if !crossed {
		return ""
	}
	w, _ := Binding(r)
	reset := Clock(w.ResetsAt, now)
	text := fmt.Sprintf("%s's %s window is at %d%%, resetting %s. ", Title(provider), WindowTitle(w.Name),
		int(math.Floor(w.Used)), reset)
	if c, ok := s.Choice(provider, now); ok {
		text += fmt.Sprintf("His answer so far: %s until %s. ", c.Choice, Clock(c.Until, now))
	}
	return text + fmt.Sprintf("Ask Wesley once, in your reply, which he wants until then: (a) keep the usual model tiers; "+
		"(b) start new sub-agents one tier down%s, while reviewers keep theirs; (c) postpone the work that needs the "+
		"usual tier until %s. Change no running agent's or session's model. Until he answers, keep the usual tiers. "+
		"Record his answer with `agent-plan usage choose %s keep|down|postpone --plan PLAN`, and for (c), once he "+
		"approves it, run `agent-plan postpone PLAN ID --until '%s'` for each step or part that waits.",
		tierHints[provider], reset, provider, time.Unix(w.ResetsAt, 0).In(now.Location()).Format("2006-01-02 15:04"))
}

// mark creates the ask's mark name, and reports whether this call made it:
// the file is created exclusively, so of sessions racing, one asks.
func (s State) mark(name string) bool {
	dir := filepath.Join(s.Dir, "asked")
	if os.MkdirAll(dir, 0o755) != nil {
		return false
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// clearMarks removes the marks of windows that have reset.
func (s State) clearMarks(now time.Time) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "asked"))
	if err != nil {
		return
	}
	for _, e := range entries {
		fields := strings.Split(e.Name(), "-")
		if len(fields) != 4 {
			continue
		}
		if resets, err := strconv.ParseInt(fields[2], 10, 64); err == nil && resets <= now.Unix() {
			os.Remove(filepath.Join(s.Dir, "asked", e.Name()))
		}
	}
}
