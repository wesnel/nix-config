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
}

type task struct{ kind, description string }

// NewFormatter prints to w.
func NewFormatter(w io.Writer) *Formatter {
	return &Formatter{w: w, tools: map[string]string{}, tasks: map[string]task{}}
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
	f := NewFormatter(w)
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
				if l = strings.TrimSpace(l); l != "" {
					f.println("stderr: " + cut(l))
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
