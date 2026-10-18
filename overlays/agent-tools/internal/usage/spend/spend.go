// Package spend counts the tokens Claude Code's transcripts record, and
// attributes them by heuristics: the session's category, repository and
// plan, and each sub-agent's role.
//
// It reads, of a transcript, only the assistant records' usage, model,
// request ID, session ID, timestamp, stop reason, working directory and
// entrypoint, and the cost-state records' session ID and token counts by
// model, never their dollar figures; and of a sub-agent's meta file only
// its type and the first word of its description, which it maps to a role
// from a fixed set. Nothing else of a
// transcript is kept, so a report holds numbers and labels alone: labels
// come from fixed sets, plan names and repository paths that pass
// labelPart.
package spend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	quota "github.com/wesnel/agent-tools/internal/usage"
)

// Tokens are a request's tokens by kind. A cache write a transcript does not
// split by lifetime counts as 5m, the API's default.
type Tokens struct {
	Input        int64 `json:"input"`
	CacheWrite5m int64 `json:"cacheWrite5m"`
	CacheWrite1h int64 `json:"cacheWrite1h"`
	CacheRead    int64 `json:"cacheRead"`
	Output       int64 `json:"output"`
}

// Total is every kind summed.
func (t Tokens) Total() int64 {
	return t.Input + t.CacheWrite5m + t.CacheWrite1h + t.CacheRead + t.Output
}

func (t Tokens) cacheWrite() int64 { return t.CacheWrite5m + t.CacheWrite1h }

func (t *Tokens) add(u Tokens) {
	t.Input += u.Input
	t.CacheWrite5m += u.CacheWrite5m
	t.CacheWrite1h += u.CacheWrite1h
	t.CacheRead += u.CacheRead
	t.Output += u.Output
}

// max raises each kind of t to u's where u's is larger. Claude Code writes a
// response as one record per content block, each with the response's usage,
// and a sub-agent's as streaming partials whose output grows.
func (t *Tokens) max(u Tokens) {
	t.Input = max(t.Input, u.Input)
	t.CacheWrite5m = max(t.CacheWrite5m, u.CacheWrite5m)
	t.CacheWrite1h = max(t.CacheWrite1h, u.CacheWrite1h)
	t.CacheRead = max(t.CacheRead, u.CacheRead)
	t.Output = max(t.Output, u.Output)
}

// Source is a Claude config directory, whose projects/ holds transcripts.
// Category, where set, is every session's in it.
type Source struct{ Dir, Category string }

// Options are what Collect reads and the period it reports.
type Options struct {
	Sources []Source
	// ReviewRoot is the directory of review directories, OWNER/NAME/N/:
	// sessions there are reviews of OWNER/NAME.
	ReviewRoot string
	// Plans maps session IDs to the plans that name them.
	Plans map[string]string
	// Since and Until bound the period reported; the one of the same
	// length before it is the previous period.
	Since, Until time.Time
}

// Dimensions are what Collect groups by.
var Dimensions = []string{"category", "plan", "repo", "role"}

// Unknown labels what no heuristic attributes.
const Unknown = "unknown"

// Row is one label's tokens in the period.
type Row struct {
	Label    string `json:"label"`
	Sessions int    `json:"sessions"`
	// Agents are the sub-agents that made requests.
	Agents   int    `json:"agents"`
	Requests int    `json:"requests"`
	Tokens   Tokens `json:"tokens"`
	Total    int64  `json:"total"`
	// OutputLowerBound is set when a request's records were all streaming
	// partials, so its output is at least what is counted.
	OutputLowerBound bool `json:"outputLowerBound"`
	// PostIdleCacheWrite is the cache write of requests made more than
	// IdleGap after the agent's previous request, once the prompt cache
	// had expired.
	PostIdleCacheWrite int64 `json:"postIdleCacheWrite"`
	// Share is the row's percent of the period's total.
	Share float64 `json:"share"`
	// Previous is the label's total in the previous period.
	Previous int64 `json:"previous"`
}

// Session is one session's tokens in the period, its sub-agents' included.
type Session struct {
	ID       string    `json:"id"`
	Started  time.Time `json:"started"`
	Category string    `json:"category"`
	// Repo is the repository with most of the session's tokens.
	Repo     string `json:"repo"`
	Plan     string `json:"plan"`
	Agents   int    `json:"agents"`
	Requests int    `json:"requests"`
	Total    int64  `json:"total"`
	// Recorded is the session's whole total: its last cost-state and the
	// records after it, or its records where it has none (Live) or a
	// Short one; see TotalRow.
	Recorded int64 `json:"recorded"`
	Live     bool  `json:"live"`
	Short    bool  `json:"short"`
}

// Report is the period's tokens grouped by one dimension.
type Report struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	By    string    `json:"by"`
	// Rows run from the largest total down, with Unknown last.
	Rows  []Row `json:"rows"`
	Total Row   `json:"total"`
	// Top are the sessions with the largest totals, at most TopSessions.
	Top []Session `json:"top"`
	// Quota is each provider's last reading, where one is known.
	Quota []quota.Reading `json:"quota,omitempty"`
	// Unreadable counts transcript and meta files that could not be read.
	Unreadable int `json:"unreadable"`
	// Totals are the sessions' recorded totals by the label of each
	// session's first main request, largest first, Unknown last; none by
	// role, since a cost-state is a whole session's. TotalsAll is every
	// session's, and Models are the totals by model.
	Totals    []TotalRow `json:"totals"`
	TotalsAll TotalRow   `json:"totalsAll"`
	Models    []ModelRow `json:"models"`
	// Coordination is how many requests main sessions made per sub-agent.
	Coordination Coordination `json:"coordination"`
	// PostIdleByRole is each role's post-idle cache writes, main first,
	// then Roles, those with no cache writes left out.
	PostIdleByRole []RoleIdle `json:"postIdleByRole"`
}

// Coordination is, over the period's sessions with sub-agents, the main
// session's requests from its first sub-agent's request on, and the
// sub-agents: a coordinator's requests per agent report.
type Coordination struct {
	Sessions     int     `json:"sessions"`
	MainRequests int     `json:"mainRequests"`
	Agents       int     `json:"agents"`
	PerAgent     float64 `json:"perAgent"`
}

// RoleIdle is a role's cache writes in the period and those made after the
// agent's cache had expired.
type RoleIdle struct {
	Role               string  `json:"role"`
	CacheWrite         int64   `json:"cacheWrite"`
	PostIdleCacheWrite int64   `json:"postIdleCacheWrite"`
	Share              float64 `json:"share"`
}

// IdleGap is the prompt cache's shorter lifetime, and IdleGap1h its
// longer: a request after a longer gap than the lifetime its agent last
// wrote writes its context to the cache again.
const (
	IdleGap   = 5 * time.Minute
	IdleGap1h = time.Hour
)

// TopSessions is how many sessions Report.Top lists.
const TopSessions = 10

// request is one API request: its records' usage, per kind the largest.
type request struct {
	session, agent, role string
	cwd, entrypoint      string
	category, model      string
	at                   time.Time
	tokens               Tokens
	final                bool
	postIdle             bool
	// line is the request's first line in its own session's transcript,
	// counted from 1, or 0 where it is in none: a sub-agent's, or one only
	// a forked session's transcript repeats.
	line int
}

// record is the part of a transcript line Collect reads.
type record struct {
	Type       string    `json:"type"`
	SessionID  string    `json:"sessionId"`
	RequestID  string    `json:"requestId"`
	UUID       string    `json:"uuid"`
	Timestamp  time.Time `json:"timestamp"`
	Cwd        string    `json:"cwd"`
	Entrypoint string    `json:"entrypoint"`
	Message    struct {
		Model      string  `json:"model"`
		StopReason *string `json:"stop_reason"`
		Usage      *struct {
			Input         int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Output        int64 `json:"output_tokens"`
			Split         *struct {
				M5 int64 `json:"ephemeral_5m_input_tokens"`
				H1 int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

func (r record) tokens() Tokens {
	u := r.Message.Usage
	t := Tokens{Input: u.Input, CacheRead: u.CacheRead, Output: u.Output, CacheWrite5m: u.CacheCreation}
	if u.Split != nil && u.Split.M5+u.Split.H1 == u.CacheCreation {
		t.CacheWrite5m, t.CacheWrite1h = u.Split.M5, u.Split.H1
	}
	return t
}

var assistantType = []byte(`"type":"assistant"`)

// scanner gathers the requests of every transcript, from every line of a
// file written since from or of a sub-agent of a session whose transcript
// was, and the last cost-state of each session's transcript.
type scanner struct {
	from       time.Time
	requests   map[string]*request
	order      []string
	states     map[string]costState
	unreadable int
}

// transcript adds the requests of one transcript file, unless skipOld
// and it was last written before from. Of a session's own transcript,
// named for it, it keeps the last cost-state naming the session, with
// its line and the time of the first line after it that has one.
func (s *scanner) transcript(path, session, agent, role, category string, skipOld bool) {
	f, err := os.Open(path)
	if err != nil {
		s.unreadable++
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		s.unreadable++
		return
	} else if skipOld && info.ModTime().Before(s.from) {
		return
	}
	lines := bufio.NewReader(f)
	// next is whether the next line's time is the last cost-state's next.
	next := false
	for n := 1; ; n++ {
		line, err := lines.ReadBytes('\n')
		switch {
		case bytes.Contains(line, assistantType):
			own := 0
			if agent == "" {
				own = n
			}
			s.line(line, session, agent, role, category, own)
		case agent == "" && bytes.Contains(line, costStateType):
			if id, c, ok := parseCostState(line); ok && id == session {
				c.line = n
				s.states[session], next = c, true
				line = nil
			}
		}
		if next && len(line) > 0 {
			var at struct {
				Timestamp time.Time `json:"timestamp"`
			}
			if json.Unmarshal(line, &at) == nil && !at.Timestamp.IsZero() {
				c := s.states[session]
				c.next, next = at.Timestamp, false
				s.states[session] = c
			}
		}
		if err != nil {
			if err != io.EOF {
				s.unreadable++
			}
			return
		}
	}
}

// line adds an assistant record to its request: one at line own of its
// session's own transcript, or where own is 0, of another.
func (s *scanner) line(line []byte, session, agent, role, category string, own int) {
	var r record
	if json.Unmarshal(line, &r) != nil || r.Type != "assistant" || r.Message.Usage == nil {
		return
	}
	t := r.tokens()
	if t.Total() == 0 {
		return
	}
	if r.SessionID != "" && r.SessionID != session {
		own = 0
	}
	key := "req:" + r.RequestID
	if r.RequestID == "" {
		key = "uuid:" + r.UUID
	}
	q, ok := s.requests[key]
	if !ok {
		if r.SessionID != "" {
			session = r.SessionID
		}
		q = &request{session: session, agent: agent, role: role, cwd: r.Cwd, entrypoint: r.Entrypoint,
			category: category, model: label(r.Message.Model), at: r.Timestamp}
		s.requests[key] = q
		s.order = append(s.order, key)
	}
	if own > 0 && q.line == 0 {
		q.line = own
	}
	q.tokens.max(t)
	if r.Timestamp.Before(q.at) {
		q.at = r.Timestamp
	}
	if r.Message.StopReason != nil {
		q.final = true
	}
}

// source adds every transcript under a config directory: each project's
// SESSION.jsonl and SESSION/subagents/agent-ID.jsonl.
func (s *scanner) source(src Source) {
	projects := filepath.Join(src.Dir, "projects")
	dirs, err := os.ReadDir(projects)
	if err != nil {
		if !os.IsNotExist(err) {
			s.unreadable++
		}
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		project := filepath.Join(projects, d.Name())
		entries, err := os.ReadDir(project)
		if err != nil {
			s.unreadable++
			continue
		}
		for _, e := range entries {
			name := e.Name()
			switch {
			case e.Type().IsRegular() && strings.HasSuffix(name, ".jsonl"):
				s.transcript(filepath.Join(project, name), strings.TrimSuffix(name, ".jsonl"), "", "main", src.Category, true)
			case e.IsDir():
				// A session written in the period keeps all its sub-agents,
				// however long ago they ended: its cost-state holds them all.
				info, err := os.Stat(filepath.Join(project, name+".jsonl"))
				recent := err == nil && !info.ModTime().Before(s.from)
				s.subagents(filepath.Join(project, name, "subagents"), name, src.Category, !recent)
			}
		}
	}
}

var agentFile = regexp.MustCompile(`^agent-([A-Za-z0-9_-]+)\.jsonl$`)

func (s *scanner) subagents(dir, session, category string, skipOld bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		m := agentFile.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		role, ok := metaRole(filepath.Join(dir, "agent-"+m[1]+".meta.json"))
		if !ok {
			s.unreadable++
		}
		s.transcript(filepath.Join(dir, e.Name()), session, m[1], role, category, skipOld)
	}
}

// metaCap bounds how much of a meta file is read.
const metaCap = 64 << 10

// roleWords map the first word of a sub-agent's description to its role.
var roleWords = map[string]string{
	"build": "build", "implement": "build", "add": "build", "write": "build", "create": "build",
	"fix": "fix", "address": "fix", "repair": "fix",
	"review": "review", "audit": "review", "verify": "review",
	"design": "design", "plan": "design",
	"research": "research", "investigate": "research", "measure": "research", "probe": "research",
	"explore": "research", "survey": "research",
	"vcs": "vcs", "restack": "vcs", "rebase": "vcs", "sign": "vcs", "push": "vcs", "commit": "vcs", "merge": "vcs",
}

// typeRoles map the agent types whose role their type says.
var typeRoles = map[string]string{"Explore": "research", "Plan": "design"}

// Roles are the roles a sub-agent can have; the parent's is "main".
var Roles = []string{"build", "fix", "review", "design", "research", "vcs", "unclassified"}

// metaRole is a sub-agent's role, from its meta file: the first word of its
// description, else its type, else unclassified. It reports false when the
// file exists and cannot be read.
func metaRole(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "unclassified", os.IsNotExist(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, metaCap))
	if err != nil {
		return "unclassified", false
	}
	var meta struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return "unclassified", false
	}
	if words := strings.Fields(meta.Description); len(words) > 0 {
		if role, ok := roleWords[strings.ToLower(strings.Trim(words[0], ".,:;!?\"'`()[]"))]; ok {
			return role, true
		}
	}
	if role, ok := typeRoles[meta.AgentType]; ok {
		return role, true
	}
	return "unclassified", true
}

// attributor labels requests, caching what it learns of each directory.
type attributor struct {
	o       Options
	common  map[string]string
	loop    map[string]bool
	scanned map[string]bool
}

// commonDir is the git directory shared by the checkout holding dir and its
// linked worktrees, read from the files git keeps, without running git:
// the directory may be gone, or not the operator's to run git in.
func (a *attributor) commonDir(dir string) string {
	if c, ok := a.common[dir]; ok {
		return c
	}
	c := ""
	for d := filepath.Clean(dir); filepath.IsAbs(d); d = filepath.Dir(d) {
		dotgit := filepath.Join(d, ".git")
		if info, err := os.Lstat(dotgit); err == nil {
			if info.IsDir() {
				c = dotgit
			} else if info.Mode().IsRegular() {
				c = linkedCommonDir(d, dotgit)
			}
			break
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	a.common[dir] = c
	return c
}

// linkedCommonDir follows a linked worktree's .git file to its git
// directory and that directory's commondir.
func linkedCommonDir(top, dotgit string) string {
	data, err := readCapped(dotgit, 4096)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return ""
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(top, gitdir)
	}
	common, err := readCapped(filepath.Join(gitdir, "commondir"), 4096)
	if err != nil {
		return filepath.Clean(gitdir)
	}
	c := strings.TrimSpace(string(common))
	if !filepath.IsAbs(c) {
		c = filepath.Join(gitdir, c)
	}
	return filepath.Clean(c)
}

func readCapped(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}

// loopLogs adds the session IDs of the pr-loop session logs in a common
// directory, $(git rev-parse --git-common-dir)/pr-loop-session*.jsonl,
// from their first lines.
func (a *attributor) loopLogs(common string) {
	if common == "" || a.scanned[common] {
		return
	}
	a.scanned[common] = true
	logs, _ := filepath.Glob(filepath.Join(common, "pr-loop-session*.jsonl"))
	for _, path := range logs {
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if id := firstSessionID(path); id != "" {
			a.loop[id] = true
		}
	}
}

// firstLines bounds how much of a session log is read for its session ID,
// which its first line, the init event, carries.
const firstLines = 1 << 20

func firstSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	lines := bufio.NewReader(io.LimitReader(f, firstLines))
	for range 8 {
		line, err := lines.ReadBytes('\n')
		var event struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(line, &event) == nil && event.SessionID != "" {
			return event.SessionID
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// underReviews is the OWNER/NAME of a directory under the review root.
func (a *attributor) underReviews(dir string) (string, bool) {
	if a.o.ReviewRoot == "" || dir == "" {
		return "", false
	}
	rel, err := filepath.Rel(a.o.ReviewRoot, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) >= 2 && labelPart.MatchString(parts[0]) && labelPart.MatchString(parts[1]) {
		return parts[0] + "/" + parts[1], true
	}
	return Unknown, true
}

func (a *attributor) category(q *request) string {
	switch {
	case q.category != "":
		return q.category
	case a.loop[q.session]:
		return "loop"
	}
	if _, ok := a.underReviews(q.cwd); ok {
		return "review"
	}
	switch {
	case strings.Contains(q.cwd, string(filepath.Separator)+".pr-loop"+string(filepath.Separator)):
		return "loop"
	case q.entrypoint == "cli":
		return "interactive"
	case q.entrypoint == "sdk-cli":
		return "print"
	}
	return Unknown
}

// labelPart is what a path component must be to become part of a label.
var labelPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// githubRepo is OWNER/NAME of a path holding github.com/OWNER/NAME.
func githubRepo(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i := len(parts) - 3; i >= 0; i-- {
		if parts[i] == "github.com" && labelPart.MatchString(parts[i+1]) && labelPart.MatchString(parts[i+2]) {
			return parts[i+1] + "/" + parts[i+2]
		}
	}
	return ""
}

func (a *attributor) repo(cwd string) string {
	if repo, ok := a.underReviews(cwd); ok {
		return repo
	}
	if cwd == "" {
		return Unknown
	}
	if c := a.commonDir(cwd); c != "" {
		top := c
		if filepath.Base(c) == ".git" {
			top = filepath.Dir(c)
		}
		if repo := githubRepo(top); repo != "" {
			return repo
		}
	}
	if repo := githubRepo(cwd); repo != "" {
		return repo
	}
	return Unknown
}

func (a *attributor) plan(session string) string {
	if p, ok := a.o.Plans[session]; ok && labelPart.MatchString(p) {
		return p
	}
	return Unknown
}

var sessionID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// labelled is a request with its labels.
type labelled struct {
	*request
	labels map[string]string
}

// Collect reads every source's transcripts and reports the period's tokens
// grouped by by, one of Dimensions.
func Collect(o Options, by string) (Report, error) {
	if !contains(Dimensions, by) {
		return Report{}, fmt.Errorf("spend: group by one of %s, not %q", strings.Join(Dimensions, ", "), by)
	}
	if !o.Since.Before(o.Until) {
		return Report{}, fmt.Errorf("spend: the period ends before it starts")
	}
	from := o.Since.Add(-o.Until.Sub(o.Since))
	s := scanner{from: from, requests: map[string]*request{}, states: map[string]costState{}}
	for _, src := range o.Sources {
		s.source(src)
	}
	a := attributor{o: o, common: map[string]string{}, loop: map[string]bool{}, scanned: map[string]bool{}}
	for _, key := range s.order {
		if q := s.requests[key]; q.cwd != "" {
			a.loopLogs(a.commonDir(q.cwd))
		}
	}
	reqs := make([]labelled, 0, len(s.order))
	for _, key := range s.order {
		q := s.requests[key]
		if !sessionID.MatchString(q.session) {
			q.session = Unknown
		}
		reqs = append(reqs, labelled{q, map[string]string{
			"category": a.category(q), "repo": a.repo(q.cwd), "plan": a.plan(q.session), "role": q.role}})
	}
	markIdle(reqs)

	r := Report{Since: o.Since, Until: o.Until, By: by, Unreadable: s.unreadable, Top: []Session{}}
	rows := map[string]*acc{}
	total := newAcc("total")
	for _, q := range reqs {
		if q.at.Before(from) {
			continue
		}
		label := q.labels[by]
		if rows[label] == nil {
			rows[label] = newAcc(label)
		}
		switch {
		case !q.at.Before(o.Since) && q.at.Before(o.Until):
			rows[label].add(q)
			total.add(q)
		case q.at.Before(o.Since):
			rows[label].Previous += q.tokens.Total()
			total.Previous += q.tokens.Total()
		}
	}
	r.Total = total.row(total.Total)
	for _, x := range rows {
		r.Rows = append(r.Rows, x.row(total.Total))
	}
	sort.Slice(r.Rows, func(i, j int) bool {
		a, b := r.Rows[i], r.Rows[j]
		if (a.Label == Unknown) != (b.Label == Unknown) {
			return b.Label == Unknown
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Label < b.Label
	})
	r.Top = top(reqs, o.Since, o.Until)
	whole := sessionTotals(&r, reqs, s.states, from, o.Since, o.Until)
	for i := range r.Top {
		w := whole[r.Top[i].ID]
		r.Top[i].Recorded, r.Top[i].Live, r.Top[i].Short = w.total, w.live, w.short
	}
	r.Coordination = coordination(reqs, o.Since, o.Until)
	r.PostIdleByRole = postIdleByRole(reqs, o.Since, o.Until)
	return r, nil
}

// markIdle marks each request made after the cache of the previous request
// of the same agent of the same session expired: more than IdleGap after
// it, or IdleGap1h where the agent's last cache write was a one-hour one.
func markIdle(reqs []labelled) {
	streams := map[string][]*request{}
	for _, q := range reqs {
		k := q.session + "/" + q.agent
		streams[k] = append(streams[k], q.request)
	}
	for _, s := range streams {
		sort.Slice(s, func(i, j int) bool { return s[i].at.Before(s[j].at) })
		lifetime := IdleGap
		for i := range s {
			if i > 0 {
				s[i].postIdle = s[i].at.Sub(s[i-1].at) > lifetime
			}
			switch {
			case s[i].tokens.CacheWrite1h > 0:
				lifetime = IdleGap1h
			case s[i].tokens.CacheWrite5m > 0:
				lifetime = IdleGap
			}
		}
	}
}

// coordination is Report.Coordination over the period's requests.
func coordination(reqs []labelled, since, until time.Time) Coordination {
	type sess struct {
		firstAgent time.Time
		agents     map[string]bool
		main       []time.Time
	}
	by := map[string]*sess{}
	for _, q := range reqs {
		if q.at.Before(since) || !q.at.Before(until) || q.session == Unknown {
			continue
		}
		s := by[q.session]
		if s == nil {
			s = &sess{agents: map[string]bool{}}
			by[q.session] = s
		}
		if q.agent == "" {
			s.main = append(s.main, q.at)
		} else {
			s.agents[q.agent] = true
			if s.firstAgent.IsZero() || q.at.Before(s.firstAgent) {
				s.firstAgent = q.at
			}
		}
	}
	var c Coordination
	for _, s := range by {
		if len(s.agents) == 0 {
			continue
		}
		c.Sessions++
		c.Agents += len(s.agents)
		for _, at := range s.main {
			if !at.Before(s.firstAgent) {
				c.MainRequests++
			}
		}
	}
	if c.Agents > 0 {
		c.PerAgent = float64(c.MainRequests) / float64(c.Agents)
	}
	return c
}

// postIdleByRole is Report.PostIdleByRole over the period's requests.
func postIdleByRole(reqs []labelled, since, until time.Time) []RoleIdle {
	by := map[string]*RoleIdle{}
	for _, q := range reqs {
		if q.at.Before(since) || !q.at.Before(until) {
			continue
		}
		x := by[q.role]
		if x == nil {
			x = &RoleIdle{Role: q.role}
			by[q.role] = x
		}
		x.CacheWrite += q.tokens.cacheWrite()
		if q.postIdle {
			x.PostIdleCacheWrite += q.tokens.cacheWrite()
		}
	}
	out := []RoleIdle{}
	for _, role := range append([]string{"main"}, Roles...) {
		if x := by[role]; x != nil && x.CacheWrite > 0 {
			x.Share = 100 * float64(x.PostIdleCacheWrite) / float64(x.CacheWrite)
			out = append(out, *x)
		}
	}
	return out
}

type acc struct {
	Row
	sessions, agents map[string]bool
}

func newAcc(label string) *acc {
	return &acc{Row: Row{Label: label}, sessions: map[string]bool{}, agents: map[string]bool{}}
}

func (x *acc) add(q labelled) {
	x.Requests++
	x.Tokens.add(q.tokens)
	x.Total += q.tokens.Total()
	x.sessions[q.session] = true
	if q.agent != "" {
		x.agents[q.session+"/"+q.agent] = true
	}
	if !q.final {
		x.OutputLowerBound = true
	}
	if q.postIdle {
		x.PostIdleCacheWrite += q.tokens.cacheWrite()
	}
}

func (x *acc) row(total int64) Row {
	r := x.Row
	r.Sessions, r.Agents = len(x.sessions), len(x.agents)
	if total > 0 {
		r.Share = 100 * float64(r.Total) / float64(total)
	}
	return r
}

func top(reqs []labelled, since, until time.Time) []Session {
	type sess struct {
		Session
		agents map[string]bool
		repos  map[string]int64
		first  time.Time
	}
	by := map[string]*sess{}
	for _, q := range reqs {
		if q.at.Before(since) || !q.at.Before(until) || q.session == Unknown {
			continue
		}
		s := by[q.session]
		if s == nil {
			s = &sess{Session: Session{ID: q.session, Started: q.at, Plan: q.labels["plan"]},
				agents: map[string]bool{}, repos: map[string]int64{}, first: q.at}
			by[q.session] = s
		}
		if q.at.Before(s.Started) {
			s.Started = q.at
		}
		if q.agent == "" && (s.Category == "" || q.at.Before(s.first)) {
			s.Category, s.first = q.labels["category"], q.at
		} else if s.Category == "" {
			s.Category = q.labels["category"]
		}
		if q.agent != "" {
			s.agents[q.agent] = true
		}
		s.repos[q.labels["repo"]] += q.tokens.Total()
		s.Requests++
		s.Total += q.tokens.Total()
	}
	out := []Session{}
	for _, s := range by {
		s.Agents = len(s.agents)
		var most int64 = -1
		for repo, n := range s.repos {
			if n > most || n == most && repo < s.Repo {
				s.Repo, most = repo, n
			}
		}
		out = append(out, s.Session)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > TopSessions {
		out = out[:TopSessions]
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// count is n with a K, M or G suffix.
func count(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%.2fG", float64(n)/1e9)
}

func change(now, before int64) string {
	switch {
	case before == 0 && now == 0:
		return "-"
	case before == 0:
		return "new"
	}
	return fmt.Sprintf("%+.0f%%", 100*float64(now-before)/float64(before))
}

func percent(part, whole int64) string {
	if whole == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(part)/float64(whole))
}

// Text writes the report as tables.
func (r Report) Text(w io.Writer) {
	const day = "2006-01-02 15:04"
	fmt.Fprintf(w, "Claude Code tokens by %s, %s to %s\n\n", r.By, r.Since.Local().Format(day), r.Until.Local().Format(day))
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(t, "%s\tsessions\tagents\trequests\tinput\tcache write 5m\tcache write 1h\tcache read\toutput\ttotal\tshare\tvs previous\tpost-idle write\t\n", r.By)
	lowerBound := false
	for _, x := range append(append([]Row{}, r.Rows...), r.Total) {
		output := count(x.Tokens.Output)
		if x.OutputLowerBound {
			output, lowerBound = "≥"+output, true
		}
		fmt.Fprintf(t, "%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%.1f%%\t%s\t%s\t\n", x.Label, x.Sessions, x.Agents, x.Requests,
			count(x.Tokens.Input), count(x.Tokens.CacheWrite5m), count(x.Tokens.CacheWrite1h), count(x.Tokens.CacheRead),
			output, count(x.Total), x.Share, change(x.Total, x.Previous), percent(x.PostIdleCacheWrite, x.Tokens.cacheWrite()))
	}
	t.Flush()
	fmt.Fprintln(w)
	if lowerBound {
		fmt.Fprintln(w, "≥: some requests left only streaming partials, so their output is at least this.")
	}
	fmt.Fprintf(w, "post-idle write: the share of cache writes made once the agent's cache had expired: more than %.0f minutes "+
		"after its previous request, or an hour after a one-hour cache write.\n", IdleGap.Minutes())
	if c := r.Coordination; c.Agents > 0 {
		fmt.Fprintf(w, "Coordinator requests per sub-agent: %.1f (%d main requests from the first sub-agent's on, %d sub-agents, %d sessions).\n",
			c.PerAgent, c.MainRequests, c.Agents, c.Sessions)
	}
	if len(r.PostIdleByRole) > 0 {
		var roles []string
		for _, x := range r.PostIdleByRole {
			roles = append(roles, fmt.Sprintf("%s %s of %s", x.Role, percent(x.PostIdleCacheWrite, x.CacheWrite), count(x.CacheWrite)))
		}
		fmt.Fprintf(w, "Post-idle cache writes by role: %s.\n", strings.Join(roles, ", "))
	}
	if r.Unreadable > 0 {
		fmt.Fprintf(w, "%d files could not be read.\n", r.Unreadable)
	}
	r.totalsText(w)

	if len(r.Top) > 0 {
		fmt.Fprintf(w, "\nTop sessions\n\n")
		t = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(t, "session\tstarted\tcategory\trepo\tplan\tagents\trequests\ttotal\twhole session")
		for _, s := range r.Top {
			id := s.ID
			if len(id) > 8 {
				id = id[:8]
			}
			recorded := count(s.Recorded)
			switch {
			case s.Short:
				recorded += " (short)"
			case s.Live:
				recorded += " (still running)"
			}
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", id, s.Started.Local().Format(day), s.Category, s.Repo, s.Plan,
				s.Agents, s.Requests, count(s.Total), recorded)
		}
		t.Flush()
	}

	if len(r.Quota) > 0 {
		fmt.Fprintf(w, "\nQuota\n\n")
		for _, q := range r.Quota {
			var windows []string
			for _, x := range q.Windows {
				windows = append(windows, fmt.Sprintf("%s %.0f%% (resets %s)", x.Name, x.Used,
					time.Unix(x.ResetsAt, 0).Local().Format(day)))
			}
			fmt.Fprintf(w, "%s: %s, read %s\n", q.Provider, strings.Join(windows, ", "),
				time.Unix(q.ObservedAt, 0).Local().Format(day))
		}
	}
}
