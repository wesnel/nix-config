package plans

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A line the operator pasted with ! runs where no hook sees it, in Claude
// Code and in Codex alike, so remind finds it in the session's transcript
// at the next event that comes with the transcript's path:
//
//   - Codex records the line with its exit code and starts no turn for it,
//     so the next UserPromptSubmit files a retro question in the session's
//     plan for each line with a non-zero exit code since the agent last
//     spoke, and tells the agent its ID.
//   - Claude Code records no exit code, and stderr alone is no failure
//     (git sign-pass reports its progress there). It does start a turn for
//     the line, so Stop, after the agent's reply to a line that wrote to
//     stderr, blocks once and asks the agent to judge and file it.
//
// The retro skill says what such a question holds.

// hookTranscript is what both harnesses send these hooks.
type hookTranscript struct {
	Event      string `json:"hook_event_name"`
	SessionID  string `json:"session_id"`
	AgentID    string `json:"agent_id"`
	Cwd        string `json:"cwd"`
	Transcript string `json:"transcript_path"`
	StopActive bool   `json:"stop_hook_active"`
}

// pastedLine is a line the operator ran with !, and where the transcript
// records it: its line number there, from 1.
type pastedLine struct {
	Command, Exit string
	At            int
}

// A transcript line can be long (a tool's whole output), up to this.
const maxTranscriptLine = 64 << 20

// eachTranscriptLine calls f with each line of the transcript at path, of
// at most limit bytes.
func eachTranscriptLine(path string, limit int, f func([]byte)) error {
	file, err := os.Open(path) // #nosec G304 -- the harness names its own transcript.
	if err != nil {
		return err
	}
	defer file.Close()
	s := bufio.NewScanner(file)
	s.Buffer(make([]byte, 0, min(limit, 1<<20)), limit)
	for s.Scan() {
		f(s.Bytes())
	}
	return s.Err()
}

// notifies matches a line that reports its own result to the session, as
// git sign-pass does with --notify, a refusal included; such a line is
// neither filed nor asked about.
var notifies = regexp.MustCompile(`(^|\s)--notify(\s|=|$)`)

var codexShell = regexp.MustCompile(`(?s)^<user_shell_command>\s*<command>\n(.*?)\n</command>\s*<result>\s*Exit code: (\d+)`)

// codexFailedLines are the lines the operator ran with ! in a Codex
// rollout since the agent's last message that exited non-zero; none when
// the rollout cannot be read to its end.
func codexFailedLines(path string, limit int) []pastedLine {
	var failed []pastedLine
	at := 0
	err := eachTranscriptLine(path, limit, func(line []byte) {
		at++
		var r struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil || r.Type != "response_item" || r.Payload.Type != "message" {
			return
		}
		switch r.Payload.Role {
		case "assistant":
			failed = nil
		case "user":
			for _, c := range r.Payload.Content {
				if m := codexShell.FindStringSubmatch(c.Text); m != nil && m[2] != "0" && !notifies.MatchString(m[1]) {
					failed = append(failed, pastedLine{Command: m[1], Exit: m[2], At: at})
				}
			}
		}
	})
	if err != nil {
		return nil
	}
	return failed
}

var (
	claudeBashInput  = regexp.MustCompile(`(?s)^<bash-input>(.*)</bash-input>$`)
	claudeBashStderr = regexp.MustCompile(`(?s)^<bash-stdout>.*</bash-stdout><bash-stderr>(.*)</bash-stderr>$`)
)

// claudeAnsweredLine is the line the operator ran with ! that the agent's
// last turn answered in a Claude Code transcript, when that line wrote to
// stderr: the last input of the main thread that is not a tool's result is
// that line's output. None when the transcript cannot be read to its end.
func claudeAnsweredLine(path string, limit int) (pastedLine, bool) {
	var last, command string
	err := eachTranscriptLine(path, limit, func(line []byte) {
		var r struct {
			Type      string `json:"type"`
			Sidechain bool   `json:"isSidechain"`
			Meta      bool   `json:"isMeta"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &r) != nil || r.Type != "user" || r.Sidechain || r.Meta {
			return
		}
		// A tool's result is a list; the operator's input is a string.
		var text string
		if json.Unmarshal(r.Message.Content, &text) != nil {
			return
		}
		if m := claudeBashInput.FindStringSubmatch(text); m != nil {
			command = strings.TrimSpace(m[1])
		}
		last = text
	})
	m := claudeBashStderr.FindStringSubmatch(last)
	if err != nil || m == nil || strings.TrimSpace(m[1]) == "" || command == "" || notifies.MatchString(command) {
		return pastedLine{}, false
	}
	return pastedLine{Command: command}, true
}

// stopAfterPastedLine is Claude Code's Stop output asking the agent to
// judge the line it just answered, or nil.
func (c Config) stopAfterPastedLine(in hookTranscript) []byte {
	if in.Event != "Stop" || in.StopActive || in.Transcript == "" {
		return nil
	}
	line, ok := claudeAnsweredLine(in.Transcript, maxTranscriptLine)
	if !ok {
		return nil
	}
	plan, where := "PLAN", " in the session's plan once it has one"
	top, _ := checkout(in.Cwd)
	if p, _, found := c.Locate(in.SessionID, top); found {
		plan, where = p.File(), ""
	}
	reason := fmt.Sprintf("The operator ran this line with ! and it wrote to stderr:\n\n%s\n\n"+
		"If it failed, file the papercut now, as the retro skill says: `agent-plan ask %s --kind retro`%s, with the line, "+
		"what it printed and the source of the bad line (the instruction or tool that produced it, or the sandbox), "+
		"with any secret in them as <REDACTED>. "+
		"Hand the operator a corrected line if one is still needed. If it did what it was for, say nothing of this and end your turn.",
		line.Command, plan, where)
	data, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
	return data
}

// fileFailedLines files a retro question for each line Codex ran for the
// operator with ! that failed since the agent last spoke, and returns the
// context that tells the agent so, or "". The question names where the
// rollout records the line, never the line: the plan is pushed to the
// notes repository, and nobody reads the line before it is filed, so a
// secret on it would go there with it.
func (c Config) fileFailedLines(in hookTranscript, now time.Time) string {
	if in.Event != "UserPromptSubmit" || in.AgentID != "" || in.Transcript == "" {
		return ""
	}
	failed := codexFailedLines(in.Transcript, maxTranscriptLine)
	if len(failed) == 0 {
		return ""
	}
	top, _ := checkout(in.Cwd)
	p, part, found := c.Locate(in.SessionID, top)
	// The text of every retro question, settled ones too.
	var filed []string
	if found {
		_ = p.questionHeadings(true, func(_ string, h heading) {
			if h.Props["KIND"] == "retro" {
				filed = append(filed, strings.Join(h.Body, "\n"))
			}
		})
	}
	var notes []string
	for _, l := range failed {
		// The line's place in the rollout identifies it: a turn aborted
		// before the agent spoke leaves it for the next prompt to see again.
		at := fmt.Sprintf("%s:%d", in.Transcript, l.At)
		if slices.ContainsFunc(filed, func(body string) bool { return strings.Contains(body, at+".") }) {
			continue
		}
		if found {
			asked, err := p.Ask(AskOptions{Part: part, Kind: "retro",
				Question:       "Fix the source of a line the operator ran with ! that failed",
				Recommendation: "Find what produced the line (an instruction, a tool's message, or the sandbox) and change it there, so the next hand-off works",
				Context:        fmt.Sprintf("The operator ran a line with ! in Codex, and it exited %s. The rollout records it at %s.", l.Exit, at),
			}, now)
			if err == nil {
				notes = append(notes, fmt.Sprintf("The operator's line `%s` exited %s; it is filed as %s in %s. "+
					"Once you know the change at its source, ask it with `agent-plan ask %s --kind retro`, any secret on the line "+
					"as <REDACTED>, and drop %s with `agent-plan decide %s %s --drop --answer 'replaced by QN'`, as the retro skill says.",
					l.Command, l.Exit, asked.ID, asked.File, p.File(), asked.ID, p.File(), asked.ID))
				continue
			}
		}
		notes = append(notes, fmt.Sprintf("The operator's line `%s` exited %s. File it as a papercut with "+
			"`agent-plan ask PLAN --kind retro`, as the retro skill says, in the session's plan once it has one.", l.Command, l.Exit))
	}
	return strings.Join(notes, "\n\n")
}
