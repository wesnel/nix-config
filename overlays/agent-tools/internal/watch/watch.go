// Package watch follows a review-loop session's stream-json log, as
// `claude -p --output-format stream-json --verbose` writes it, and prints
// it for a person: the agents' text, each tool call with a short form of
// its arguments, each result cut to a line or two, refusals marked, and
// sub-agents' starts and ends. Everything printed passes through secrets,
// and its control characters are shown as text (println).
package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// width is the most runes printed of one line from the log.
const width = 160

// Formatter prints the events of one log.
type Formatter struct {
	w io.Writer
	// tools names each tool call by its ID, for its result.
	tools map[string]string
	// tasks describes each task Claude Code runs beside the turn, a
	// sub-agent or a background command, by its ID, for its end.
	tasks map[string]task
	// commands holds each shell call's command by its ID.
	commands map[string]string
	// events are the events printed, or nil for everything.
	events map[string]bool
}

type task struct{ kind, description string }

// NewFormatter prints to w.
func NewFormatter(w io.Writer) *Formatter {
	return &Formatter{w: w, tools: map[string]string{}, tasks: map[string]task{}, commands: map[string]string{}}
}

// Events are what FollowEvents can print alone: proposal, each line `pr-loop
// propose` printed starting ProposalMarker, read only from that command's
// own result, since anything else the session ran or read could print the
// same words; and handback, the line a loop launcher writes on stderr once
// the session has exited.
var Events = []string{"proposal", "handback"}

// ProposalMarker starts the line `pr-loop propose` prints for a proposal.
const ProposalMarker = "pr-loop: proposal "

// simple reports whether a shell command line is one command with plain
// arguments: outside quotes no list, pipe, background, redirection,
// subshell or line break; nowhere a command substitution; every quote
// closed. Single quotes take everything literally; double quotes all but
// `$(`, `${` and backticks, which simple refuses.
func simple(command string) bool {
	var quote rune
	escaped := false
	runes := []rune(command)
	for i, r := range runes {
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case escaped:
			if r == '\n' {
				return false
			}
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			}
		case r == '`', r == '$' && (next == '(' || next == '{'):
			return false
		case quote == '"':
			if r == '\\' {
				escaped = true
			} else if r == '"' {
				quote = 0
			}
		case r == '\\':
			escaped = true
		case r == '\'' || r == '"':
			quote = r
		case strings.ContainsRune(";&|<>()\n\r", r):
			return false
		}
	}
	return quote == 0 && !escaped
}

// handBack matches the line claude-pr-loop and codex-pr-loop write.
var handBack = regexp.MustCompile(`^[a-z]+-pr-loop: the loop session exited`)

// proposal prints the proposal lines in the result of a call, by its ID,
// that ran `pr-loop propose` alone.
func (f *Formatter) proposal(id string, content json.RawMessage) {
	if command := strings.TrimSpace(f.commands[id]); !strings.HasPrefix(command, "pr-loop propose ") || !simple(command) {
		return
	}
	for _, l := range strings.Split(secrets(resultText(content)), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, ProposalMarker) {
			f.println("proposal: " + cut(l))
		}
	}
}

type event struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	TaskDescription string          `json:"task_description"`
	Message         json.RawMessage `json:"message"`

	// system
	Cwd         string `json:"cwd"`
	Model       string `json:"model"`
	SessionID   string `json:"session_id"`
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`

	// result
	IsError    bool    `json:"is_error"`
	NumTurns   int     `json:"num_turns"`
	DurationMS int64   `json:"duration_ms"`
	Cost       float64 `json:"total_cost_usd"`
	Result     string  `json:"result"`
}

type message struct {
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// Event prints one line of the log.
func (f *Formatter) Event(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var e event
	if err := json.Unmarshal(line, &e); err != nil {
		f.println("?? a line that is not stream-json")
		return
	}
	if f.events != nil {
		f.filtered(e)
		return
	}
	prefix := ""
	if e.ParentToolUseID != nil && e.TaskDescription != "" {
		prefix = "[" + oneLine(e.TaskDescription) + "] "
	}
	switch e.Type {
	case "system":
		f.system(e)
	case "assistant", "user":
		var m message
		if json.Unmarshal(e.Message, &m) != nil {
			return
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			if e.Type == "user" && prefix == "" {
				f.lines("", ">> ", "   ", text, 2)
			}
			return
		}
		var blocks []block
		if json.Unmarshal(m.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			f.block(e.Type, prefix, b)
		}
	case "result":
		head := fmt.Sprintf("== result %s after %d turns, %ds, $%.2f: ", e.Subtype, e.NumTurns, e.DurationMS/1000, e.Cost)
		if e.IsError {
			head = "!! " + strings.TrimPrefix(head, "== ")
		}
		f.lines("", head, "   ", e.Result, 2)
	}
}

// filtered prints what of e the events name: only proposals, since the
// hand-back is on stderr.
func (f *Formatter) filtered(e event) {
	if !f.events["proposal"] || (e.Type != "assistant" && e.Type != "user") {
		return
	}
	var m message
	var blocks []block
	if json.Unmarshal(e.Message, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch {
		case e.Type == "assistant" && b.Type == "tool_use":
			var in struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(b.Input, &in) == nil {
				f.commands[b.ID] = in.Command
			}
		case e.Type == "user" && b.Type == "tool_result" && !b.IsError:
			f.proposal(b.ToolUseID, b.Content)
		}
	}
}

func (f *Formatter) system(e event) {
	switch e.Subtype {
	case "init":
		id, _, _ := strings.Cut(e.SessionID, "-")
		f.println(fmt.Sprintf("== session %s started in %s (%s)", id, e.Cwd, e.Model))
	case "task_started":
		kind := "background command"
		if e.TaskType == "local_agent" {
			kind = "subagent"
		}
		f.tasks[e.TaskID] = task{kind, oneLine(e.Description)}
		f.println(fmt.Sprintf("++ %s started: %s", kind, cut(secrets(oneLine(e.Description)))))
	case "task_notification":
		t, ok := f.tasks[e.TaskID]
		if !ok {
			t = task{"task", e.TaskID}
		}
		line := t.kind + " " + e.Status + ": " + t.description
		if e.Summary != "" {
			line += ": " + firstLine(e.Summary)
		}
		f.println("-- " + cut(secrets(line)))
	}
}

func (f *Formatter) block(kind, prefix string, b block) {
	switch {
	case kind == "assistant" && b.Type == "text":
		f.lines(prefix, "", "", b.Text, -1)
	case kind == "assistant" && b.Type == "tool_use":
		f.tools[b.ID] = b.Name
		f.println(fmt.Sprintf("%s-> %s: %s", prefix, b.Name, cut(secrets(arguments(b.Name, b.Input)))))
	case kind == "user" && b.Type == "tool_result":
		name := f.tools[b.ToolUseID]
		if name == "" {
			name = "tool"
		}
		text := secrets(resultText(b.Content))
		if b.IsError {
			if line := refusal(text); line != "" {
				f.println(fmt.Sprintf("%s!! REFUSED %s: %s", prefix, name, cut(line)))
				return
			}
			f.lines(prefix, "<- "+name+" failed: ", "   ", text, 2)
			return
		}
		f.lines(prefix, "<- "+name+": ", "   ", text, 2)
	case kind == "user" && b.Type == "text" && prefix == "":
		f.lines("", ">> ", "   ", b.Text, 2)
	}
}

// refused finds a line saying a call was refused: by pr-loop, the relay or
// a guard hook, or by Claude Code's permission check.
var refused = regexp.MustCompile(`(?i)\brefused\b|\bdenied\b|\bblocked\b`)

// refusal is the first line of text that says the call was refused, or "".
func refusal(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if refused.MatchString(l) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// arguments is a tool call's input, short: a command's first line, a
// file's path, a sub-agent's type and task, or else its JSON.
func arguments(tool string, raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return oneLine(string(raw))
	}
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch {
	case str("command") != "":
		c := str("command")
		first, _, more := strings.Cut(strings.TrimSpace(c), "\n")
		if more {
			first += " ..."
		}
		return first
	case tool == "Agent" || tool == "Task":
		s := str("description")
		if t := str("subagent_type"); t != "" {
			s = t + ": " + s
		}
		if bg, _ := in["run_in_background"].(bool); bg {
			s += " (background)"
		}
		return s
	case str("file_path") != "":
		return str("file_path")
	case str("skill") != "":
		return str("skill")
	case str("pattern") != "":
		return str("pattern")
	case str("to") != "":
		return "to " + str("to") + ": " + firstLine(str("summary")+str("message"))
	}
	compact, _ := json.Marshal(in)
	return string(compact)
}

// resultText is a tool result's text, whether a string or text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// lines prints text's non-blank lines, the first after head and the rest
// after more, at most max of them (all where max < 0), noting how many
// were left out. Each is redacted, then cut to width.
func (f *Formatter) lines(prefix, head, more, text string, max int) {
	var kept []string
	for _, l := range strings.Split(secrets(text), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		if head != "" {
			f.println(prefix + strings.TrimSuffix(head, " "))
		}
		return
	}
	left := 0
	if max >= 0 && len(kept) > max {
		left = len(kept) - max
		kept = kept[:max]
	}
	for i, l := range kept {
		lead := more
		if i == 0 {
			lead = head
		}
		l = cut(l)
		if i == len(kept)-1 && left > 0 {
			l += fmt.Sprintf(" [+%d lines]", left)
		}
		f.println(prefix + lead + l)
	}
}

// println prints one line, redacted again whole, prefix and all, and with
// every control character shown as text: the log holds what commands, bots
// and the sandboxed session wrote, and an escape sequence printed raw would
// act on the operator's terminal, its title, clipboard or links.
func (f *Formatter) println(line string) {
	fmt.Fprintln(f.w, visible(secrets(line)))
}

// visible is s with each C0 and C1 control character but tab, and DEL,
// shown as text: ^[ for ESC, ^? for DEL, \x9b for a C1 one.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20:
			b.WriteByte('^')
			b.WriteRune(r + 0x40)
		case r == 0x7f:
			b.WriteString("^?")
		case r >= 0x80 && r < 0xa0:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// cut shortens s to width runes.
func cut(s string) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-3]) + "..."
}

// Follow prints the log at path, and the session's stderr beside it (the
// same path ending .err for .jsonl), as they grow, until ctx is done:
// from their start, or from their end. A log not yet written is waited
// for, one cut shorter, as a new session's is, is read again from its
// start, and a line still being written is read once it ends.
func Follow(ctx context.Context, path string, fromStart bool, w io.Writer, poll time.Duration) error {
	return FollowEvents(ctx, path, fromStart, nil, w, poll)
}

// FollowEvents is Follow printing only the events named, of Events, when
// any are: each then on a line of its own after the event's name, and
// nothing else. With handback among them it returns once it has printed
// that event.
func FollowEvents(ctx context.Context, path string, fromStart bool, events []string, w io.Writer, poll time.Duration) error {
	f := NewFormatter(w)
	if len(events) > 0 {
		f.events = map[string]bool{}
		for _, e := range events {
			if !slices.Contains(Events, e) {
				return fmt.Errorf("%q is not an event watch knows; name one of %s", e, strings.Join(Events, ", "))
			}
			f.events[e] = true
		}
	}
	log := &tail{path: path, fromStart: fromStart}
	var errs *tail
	if base, ok := strings.CutSuffix(path, ".jsonl"); ok {
		errs = &tail{path: base + ".err", fromStart: fromStart}
	}
	for {
		if err := log.read(f.Event); err != nil {
			return err
		}
		if errs != nil {
			// The lines read together are redacted together, so a secret
			// spread over lines, as a key's body is, is found whole.
			var batch []string
			if err := errs.read(func(line []byte) { batch = append(batch, string(line)) }); err != nil {
				return err
			}
			for _, l := range strings.Split(secrets(strings.Join(batch, "")), "\n") {
				l = strings.TrimSpace(l)
				switch {
				case l == "":
				case f.events == nil:
					f.println("stderr: " + cut(l))
				case f.events["handback"] && handBack.MatchString(l):
					f.println("handback: " + cut(l))
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
}

// tail reads a growing file's complete lines.
type tail struct {
	path      string
	fromStart bool
	started   bool
	offset    int64
	partial   []byte
	// skip drops the line under way where reading began, from its end.
	skip bool
}

// read passes each complete line added since the last read to each.
func (t *tail) read(each func([]byte)) error {
	file, err := os.Open(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		if !t.started {
			// Whatever is written later is new.
			t.started, t.fromStart = true, true
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !t.started {
		t.started = true
		if !t.fromStart {
			t.offset = info.Size()
			t.skip = t.offset > 0 && !endsLine(file, t.offset)
		}
	}
	if info.Size() < t.offset {
		t.offset, t.partial, t.skip = 0, nil, false
	}
	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	t.offset += int64(len(data))
	data = append(t.partial, data...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if t.skip {
			t.skip = false
		} else {
			each(data[:i+1])
		}
		data = data[i+1:]
	}
	t.partial = append([]byte(nil), data...)
	return nil
}

// endsLine reports whether the byte before offset ends a line.
func endsLine(file *os.File, offset int64) bool {
	b := make([]byte, 1)
	_, err := file.ReadAt(b, offset-1)
	return err == nil && b[0] == '\n'
}
