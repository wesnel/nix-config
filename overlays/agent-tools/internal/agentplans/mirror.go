package agentplans

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/plans"
)

// Mirror binds one session's prepared bytes to the native publication receipt.
// Pending survives failed or missing receipts; preparation cannot clear it.
type Mirror struct {
	Session       string         `json:"session"`
	Plan          string         `json:"plan"`
	Title         string         `json:"title"`
	File          string         `json:"file"`
	Hash          string         `json:"hash"`
	Revision      string         `json:"revision"`
	PublishedHash string         `json:"publishedHash"`
	Backend       string         `json:"backend"`
	Page          string         `json:"page"`
	Block         string         `json:"block"`
	BlockHash     string         `json:"blockHash"`
	CreateKey     string         `json:"createKey"`
	Pending       *MirrorPending `json:"pending,omitempty"`
	Scope         string         `json:"scope,omitempty"`
}

type MirrorPending struct {
	Tool      string `json:"tool"`
	Call      string `json:"call"`
	InputHash string `json:"inputHash"`
}

func MirrorDir() string { return filepath.Join(Dir(), "mirrors") }

func mirrorFile(session string) string {
	return filepath.Join(MirrorDir(), PageHash([]byte(session))+".json")
}

func currentSession(session string) error {
	h, id := plans.EnvSession()
	if id == "" || session != h+":"+id {
		return errors.New("mirror requires the current harness session")
	}
	return nil
}

func loadMirror(session string) (Mirror, error) {
	var m Mirror
	b, err := os.ReadFile(mirrorFile(session))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	if err == nil && (m.Session != session || m.File != filepath.Join(MirrorDir(), PageHash([]byte(session))+".html")) {
		err = errors.New("invalid mirror identity")
	}
	return m, err
}

func LoadMirror(session string) (Mirror, error) {
	if err := currentSession(session); err != nil {
		return Mirror{}, err
	}
	return loadMirror(session)
}

// mirrorLock serializes only one session or the singleton aggregate owner.
func mirrorLock(key string, fn func() error) error {
	if err := os.MkdirAll(MirrorDir(), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(MirrorDir(), PageHash([]byte(key))+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func mirrorWrite(file string, data []byte) error {
	f, err := os.CreateTemp(MirrorDir(), ".mirror-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), file)
}

func saveMirror(m Mirror) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if m.Scope == "aggregate" {
		return mirrorWrite(filepath.Join(MirrorDir(), "aggregate-mirror.json"), b)
	}
	return mirrorWrite(mirrorFile(m.Session), b)
}

// PrepareMirror takes a content revision independent of the render timestamp.
// An unchanged revision reuses the existing bytes only if their exact hash matches.
func PrepareMirror(session, plan, title, revision string, html []byte) (Mirror, error) {
	var m Mirror
	if err := currentSession(session); err != nil {
		return m, err
	}
	if plan == "" || title == "" || revision == "" || len(title) > 512 || len(html) == 0 || len(html) > 256<<10 || !utf8.Valid(html) {
		return m, errors.New("mirror requires a plan, title, revision and bounded UTF-8 snapshot")
	}
	err := mirrorLock(session, func() error {
		var err error
		m, err = loadMirror(session)
		if errors.Is(err, os.ErrNotExist) {
			backend := "space"
			if strings.HasPrefix(session, "claude:") {
				backend = "claude"
			}
			m = Mirror{Session: session, Plan: plan, Title: title, Backend: backend, File: filepath.Join(MirrorDir(), PageHash([]byte(session))+".html"), CreateKey: "plan-mirror-" + PageHash([]byte(session))}
		} else if err != nil {
			return err
		}
		if m.Title != title {
			return errors.New("mirror title binding is immutable")
		}
		if m.Pending != nil {
			return errors.New("mirror publication pending; operator reconciliation required")
		}
		m.Plan = plan
		if reusableSnapshot(m, revision) {
			return saveMirror(m)
		}
		if err := mirrorWrite(m.File, html); err != nil {
			return err
		}
		m.Hash = PageHash(html)
		m.Revision = revision
		return saveMirror(m)
	})
	return m, err
}

func reusableSnapshot(m Mirror, revision string) bool {
	if m.Revision != revision || m.Hash == "" {
		return false
	}
	b, err := os.ReadFile(m.File)
	return err == nil && PageHash(b) == m.Hash
}

// BeginMirror journals an exact native mutation before the guard permits it.
// The caller supplies the session and call identity from the native hook event.
func BeginMirror(session, tool, call string, input json.RawMessage) error {
	if err := currentSession(session); err != nil {
		return err
	}
	if call == "" {
		return errors.New("native mirror call has no identity")
	}
	key := mirrorCallKey(session, tool, input)
	return mirrorLock(key, func() error {
		m, err := loadCallMirror(session, key)
		if err != nil {
			return err
		}
		if m.Pending != nil {
			return errors.New("mirror publication pending; operator reconciliation required")
		}
		if err := matchMirror(m, tool, input); err != nil {
			return err
		}
		m.Pending = &MirrorPending{Tool: tool, Call: call, InputHash: PageHash(input)}
		return saveMirror(m)
	})
}

func exactInput(input json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(input, &args); err != nil || args == nil {
		return nil, errors.New("invalid mirror input")
	}
	for k := range args {
		found := false
		for _, want := range keys {
			if k == want {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("unexpected mirror argument %s", k)
		}
	}
	return args, nil
}

func textArg(args map[string]json.RawMessage, key string) string {
	var s string
	json.Unmarshal(args[key], &s)
	return s
}

var errMirrorMismatch = errors.New("native write does not match the prepared session mirror")

func matchMirror(m Mirror, tool string, input json.RawMessage) error {
	switch tool {
	case "mcp__codex_apps__chatgpt_space__create_page":
		return matchPageCreation(m, input)
	case "mcp__codex_apps__chatgpt_space__create_page_visualization":
		return matchVisualization(m, input)
	case "Artifact":
		return matchArtifact(m, input)
	default:
		return errMirrorMismatch
	}
}

func matchPageCreation(m Mirror, input json.RawMessage) error {
	a, err := exactInput(input, "title", "idempotency_key")
	if err != nil {
		return err
	}
	if m.Backend != "space" || m.Page != "" || textArg(a, "title") != m.Title || textArg(a, "idempotency_key") != m.CreateKey {
		return errMirrorMismatch
	}
	return nil
}

func matchVisualization(m Mirror, input json.RawMessage) error {
	a, err := exactInput(input, "page_id", "title", "html", "replace_block", "base_sequence")
	if err != nil {
		return err
	}
	if m.Backend != "space" || m.Page == "" || textArg(a, "page_id") != m.Page || textArg(a, "title") != m.Title || PageHash([]byte(textArg(a, "html"))) != m.Hash {
		return errMirrorMismatch
	}
	b, err := os.ReadFile(m.File)
	if err != nil || PageHash(b) != m.Hash {
		return errMirrorMismatch
	}
	if m.Block == "" {
		if _, ok := a["replace_block"]; ok {
			return errMirrorMismatch
		}
	} else {
		r, err := exactInput(a["replace_block"], "block_id", "expected_hash")
		if err != nil || textArg(r, "block_id") != m.Block || textArg(r, "expected_hash") != m.BlockHash {
			return errMirrorMismatch
		}
	}
	return nil
}

func matchArtifact(m Mirror, input json.RawMessage) error {
	a, err := exactInput(input, "command", "url", "file_path", "capabilities")
	if err != nil {
		return err
	}
	if m.Backend != "claude" || textArg(a, "command") != "publish" || textArg(a, "url") != m.Page || textArg(a, "file_path") != m.File {
		return errMirrorMismatch
	}
	var capabilities map[string]any
	if raw, ok := a["capabilities"]; ok && (!bytes.Equal(bytes.TrimSpace(raw), []byte(`{}`)) || json.Unmarshal(raw, &capabilities) != nil) {
		return errMirrorMismatch
	}
	b, err := os.ReadFile(m.File)
	if err != nil || PageHash(b) != m.Hash {
		return errMirrorMismatch
	}
	return nil
}

// RecordMirror accepts only a matching successful native post-tool receipt.
// Unknown responses retain Pending and require operator reconciliation.
func RecordMirror(event []byte) error {
	var e struct {
		Event    string          `json:"hook_event_name"`
		Session  string          `json:"session_id"`
		Turn     string          `json:"turn_id"`
		Agent    string          `json:"agent_id"`
		Tool     string          `json:"tool_name"`
		Call     string          `json:"tool_use_id"`
		CallID   string          `json:"tool_call_id"`
		Input    json.RawMessage `json:"tool_input"`
		Response json.RawMessage `json:"tool_response"`
	}
	if err := json.Unmarshal(event, &e); err != nil {
		return err
	}
	if e.CallID != "" {
		if e.Call != "" && e.Call != e.CallID {
			return errors.New("conflicting native call identities")
		}
		e.Call = e.CallID
	}
	if e.Event != "PostToolUse" || e.Agent != "" || e.Call == "" {
		return errors.New("mirror receipt requires a native main-session PostToolUse event")
	}
	h := "claude"
	if e.Turn != "" {
		h = "codex"
	}
	session := h + ":" + e.Session
	if err := currentSession(session); err != nil {
		return err
	}
	key := mirrorCallKey(session, e.Tool, e.Input)
	return mirrorLock(key, func() error {
		m, err := loadCallMirror(session, key)
		if err != nil {
			return err
		}
		if m.Pending == nil || m.Pending.Tool != e.Tool || m.Pending.Call != e.Call || m.Pending.InputHash != PageHash(e.Input) {
			return errors.New("mirror receipt does not match pending call")
		}
		if err := applyMirrorReceipt(&m, e.Response); err != nil {
			return err
		}
		m.Pending = nil
		return saveMirror(m)
	})
}

var errMirrorReceipt = errors.New("mirror receipt is uncertain; operator reconciliation required")

type pageMirrorReceipt struct {
	Error    json.RawMessage `json:"error"`
	Page     string          `json:"page_id"`
	Metadata struct {
		Page string `json:"page_id"`
	} `json:"metadata"`
	Content struct {
		Page string `json:"page_id"`
	} `json:"content"`
	Edit struct {
		Error   json.RawMessage `json:"error"`
		Results []struct {
			Status   string          `json:"status"`
			Code     json.RawMessage `json:"error_code"`
			Ignored  json.RawMessage `json:"ignored_reason"`
			Created  []string        `json:"created_block_ids"`
			Affected []struct {
				ID   string `json:"block_id"`
				Hash string `json:"hash"`
			} `json:"affected_blocks"`
		} `json:"operation_results"`
	} `json:"edit"`
}

func applyMirrorReceipt(m *Mirror, raw json.RawMessage) error {
	if m.Pending.Tool == "Artifact" {
		return applyArtifactReceipt(m, raw)
	}
	var response struct {
		IsError    bool            `json:"isError"`
		Error      json.RawMessage `json:"error"`
		Structured json.RawMessage `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &response) != nil || response.IsError || present(response.Error) {
		return errMirrorReceipt
	}
	var r pageMirrorReceipt
	if json.Unmarshal(response.Structured, &r) != nil || present(r.Error) || present(r.Edit.Error) {
		return errMirrorReceipt
	}
	switch m.Pending.Tool {
	case "mcp__codex_apps__chatgpt_space__create_page":
		return applyPageCreationReceipt(m, r)
	case "mcp__codex_apps__chatgpt_space__create_page_visualization":
		return applyVisualizationReceipt(m, r)
	default:
		return errMirrorReceipt
	}
}

func applyArtifactReceipt(m *Mirror, raw json.RawMessage) error {
	var r struct {
		Status  string          `json:"status"`
		URL     string          `json:"url"`
		Error   json.RawMessage `json:"error"`
		IsError bool            `json:"isError"`
	}
	if json.Unmarshal(raw, &r) != nil || r.IsError || present(r.Error) || r.Status != "published" || !pageURL.MatchString(r.URL) || m.Page != "" && m.Page != r.URL {
		return errMirrorReceipt
	}
	m.Page = r.URL
	m.PublishedHash = m.Hash
	return nil
}

func applyPageCreationReceipt(m *Mirror, r pageMirrorReceipt) error {
	if !spacePage.MatchString(r.Metadata.Page) || m.Page != "" || r.Content.Page != "" && r.Content.Page != r.Metadata.Page {
		return errMirrorReceipt
	}
	m.Page = r.Metadata.Page
	return nil
}

func applyVisualizationReceipt(m *Mirror, r pageMirrorReceipt) error {
	if r.Page != m.Page || len(r.Edit.Results) != 1 {
		return errMirrorReceipt
	}
	op := r.Edit.Results[0]
	if op.Status != "applied" || present(op.Code) || present(op.Ignored) || len(op.Affected) != 1 {
		return errMirrorReceipt
	}
	b := op.Affected[0]
	if !spacePage.MatchString(b.ID) || b.Hash == "" {
		return errMirrorReceipt
	}
	if m.Block == "" {
		if len(op.Created) != 1 || op.Created[0] != b.ID {
			return errMirrorReceipt
		}
	} else if b.ID != m.Block || len(op.Created) != 0 {
		return errMirrorReceipt
	}
	m.Block, m.BlockHash = b.ID, b.Hash
	m.PublishedHash = m.Hash
	return nil
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

type aggregateOwner struct {
	Session string `json:"session"`
	Target  string `json:"target"`
}

func ClaimAggregate(session string) (string, error) {
	if err := currentSession(session); err != nil {
		return "", err
	}
	if !strings.HasPrefix(session, "claude:") {
		return "", errors.New("aggregate Artifact owner requires Claude")
	}
	targets, err := LoadTargets()
	if err != nil {
		return "", err
	}
	if targets.ClaudePage == "" {
		return "", errors.New("aggregate Artifact target is unset")
	}
	status := "claimed"
	err = mirrorLock("aggregate", func() error {
		file := filepath.Join(MirrorDir(), "aggregate.json")
		var owner aggregateOwner
		b, err := os.ReadFile(file)
		if err == nil {
			if json.Unmarshal(b, &owner) != nil {
				return errors.New("invalid aggregate owner")
			}
			if owner.Session != "" && (owner.Session != session || owner.Target != targets.ClaudePage) {
				status = "busy"
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b, err = json.Marshal(aggregateOwner{session, targets.ClaudePage})
		if err != nil {
			return err
		}
		return mirrorWrite(file, b)
	})
	return status, err
}

func ReleaseAggregate(session string) error {
	if err := currentSession(session); err != nil {
		return err
	}
	return mirrorLock("aggregate", func() error {
		file := filepath.Join(MirrorDir(), "aggregate.json")
		b, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var owner aggregateOwner
		if json.Unmarshal(b, &owner) != nil || owner.Session != "" && owner.Session != session {
			return errors.New("aggregate belongs to another session")
		}
		if m, err := loadAggregate(); err == nil && m.Pending != nil {
			return errors.New("aggregate publication pending; operator reconciliation required")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return mirrorWrite(file, []byte(`{}`))
	})
}

func loadAggregate() (Mirror, error) {
	var m Mirror
	b, err := os.ReadFile(filepath.Join(MirrorDir(), "aggregate-mirror.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	if err == nil && (m.Scope != "aggregate" || m.File != filepath.Join(MirrorDir(), "aggregate.html")) {
		err = errors.New("invalid aggregate mirror")
	}
	return m, err
}

func aggregateOwned(session string) error {
	b, err := os.ReadFile(filepath.Join(MirrorDir(), "aggregate.json"))
	if err != nil {
		return err
	}
	var owner aggregateOwner
	if json.Unmarshal(b, &owner) != nil || owner.Session != session {
		return errors.New("aggregate belongs to another session")
	}
	targets, err := LoadTargets()
	if err != nil {
		return err
	}
	if targets.ClaudePage == "" || owner.Target != targets.ClaudePage {
		return errors.New("aggregate target changed")
	}
	return nil
}

func mirrorCallKey(session, tool string, input json.RawMessage) string {
	if tool == "Artifact" {
		var a struct {
			File string `json:"file_path"`
		}
		if json.Unmarshal(input, &a) == nil && a.File == filepath.Join(MirrorDir(), "aggregate.html") {
			return "aggregate"
		}
	}
	return session
}

func loadCallMirror(session, key string) (Mirror, error) {
	if key != "aggregate" {
		return loadMirror(session)
	}
	if err := aggregateOwned(session); err != nil {
		return Mirror{}, err
	}
	m, err := loadAggregate()
	if err == nil && m.Session != session {
		return Mirror{}, errors.New("aggregate snapshot belongs to another session")
	}
	return m, err
}

// PrepareAggregate requires explicit ownership of the configured Artifact.
// Ownership has no expiration and cannot be released during an uncertain write.
// Revision has the same meaning as in PrepareMirror.
func PrepareAggregate(session, revision string, html []byte) (Mirror, error) {
	var m Mirror
	if err := currentSession(session); err != nil {
		return m, err
	}
	if revision == "" || len(html) == 0 || len(html) > 256<<10 || !utf8.Valid(html) {
		return m, errors.New("invalid aggregate snapshot")
	}
	err := mirrorLock("aggregate", func() error {
		if err := aggregateOwned(session); err != nil {
			return err
		}
		old, err := loadAggregate()
		if err == nil && old.Pending != nil {
			return errors.New("aggregate publication pending; operator reconciliation required")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		targets, err := LoadTargets()
		if err != nil {
			return err
		}
		if old.Page == targets.ClaudePage && reusableSnapshot(old, revision) {
			m = old
			m.Session = session
			return saveMirror(m)
		}
		m = Mirror{Session: session, Scope: "aggregate", Plan: "aggregate", Title: "All open plan items", File: filepath.Join(MirrorDir(), "aggregate.html"), Hash: PageHash(html), Revision: revision, Backend: "claude", Page: targets.ClaudePage}
		if old.Page == m.Page {
			m.PublishedHash = old.PublishedHash
		}
		if err := mirrorWrite(m.File, html); err != nil {
			return err
		}
		return saveMirror(m)
	})
	return m, err
}
