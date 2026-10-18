package queue

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/usage"
)

// fakeSystemdRun logs its arguments and runs the command after "--" in the
// unit's working directory, as `systemd-run --wait` would, with its exit
// status.
const fakeSystemdRun = `#!/bin/sh
printf 'systemd-run %s\n' "$*" >> "$FAKE_LOG"
dir=
while [ "$1" != "--" ]; do
  case "$1" in
    -p) case "$2" in WorkingDirectory=*) dir="${2#WorkingDirectory=}";; esac; shift 2;;
    --slice|--unit|--setenv) shift 2;;
    *) shift;;
  esac
done
shift
if [ -n "$dir" ]; then cd "$dir" || exit 200; fi
exec "$@"
`

// fakePrLoop logs its arguments, notes a second review running at once,
// turns the kill switch on where $FAKE_OFF names it, and exits with the
// status in $FAKE_STATE/exit.OWNER_NAME_N, its last argument's, or 0.
const fakePrLoop = `#!/bin/sh
echo "pr-loop $*" >> "$FAKE_LOG"
for a; do key=$a; done
if ! mkdir "$FAKE_STATE/busy" 2>/dev/null; then echo overlap >> "$FAKE_LOG"; fi
sleep "${FAKE_SLEEP:-0.2}"
rmdir "$FAKE_STATE/busy"
if [ -n "$FAKE_OFF" ]; then : > "$FAKE_OFF"; fi
f="$FAKE_STATE/exit.$(printf %s "$key" | tr '/#' '__')"
if [ -f "$f" ]; then exit "$(cat "$f")"; fi
exit 0
`

// fakeSystemctl logs its arguments; is-active succeeds while
// $FAKE_STATE/active exists.
const fakeSystemctl = `#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$FAKE_LOG"
case " $* " in *" is-active "*) [ -e "$FAKE_STATE/active" ]; exit;; esac
exit 0
`

// fakeClaudePrLoop logs where it runs and its arguments, one per line, and
// prints a rate_limit_event with the 5-hour window at $FAKE_UTIL.
const fakeClaudePrLoop = `#!/bin/sh
{ echo "claude-pr-loop in $(pwd -P)"; for a in "$@"; do echo "  arg: $a"; done; } >> "$FAKE_LOG"
echo '{"type":"system","subtype":"init"}'
echo '{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":'"$FAKE_UTIL"',"resetsAt":'"$FAKE_RESET"'}}}}'
exit "${FAKE_TRIAGE_EXIT:-0}"
`

type fixture struct {
	p      Paths
	root   string // the review directory
	state  string
	log    string
	usage  usage.State
	now    time.Time
	output bytes.Buffer
}

func setUpRunner(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{p: setUp(t), root: filepath.Join(t.TempDir(), "pr-review"), state: t.TempDir(),
		log: filepath.Join(t.TempDir(), "log"), usage: usage.State{Dir: filepath.Join(t.TempDir(), "usage")},
		now: time.Now().Truncate(time.Second)}
	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	for name, script := range map[string]string{"systemd-run": fakeSystemdRun, "pr-loop": fakePrLoop,
		"claude-pr-loop": fakeClaudePrLoop, "systemctl": fakeSystemctl} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", f.log)
	t.Setenv("FAKE_STATE", f.state)
	t.Setenv("FAKE_OFF", "")
	t.Setenv("FAKE_TRIAGE_EXIT", "")
	t.Setenv("FAKE_UTIL", "0.5")
	t.Setenv("FAKE_SLEEP", "")
	t.Setenv("FAKE_RESET", strconv.FormatInt(f.now.Add(3*time.Hour).Unix(), 10))
	config := filepath.Join(t.TempDir(), "pr-loop.json")
	os.WriteFile(config, []byte(`{"review": {"dir": "`+f.root+`"}}`), 0o644)
	t.Setenv("PR_LOOP_CONFIG", config)
	t.Setenv("AGENT_PLANS_SESSION", "1")
	return f
}

func (f *fixture) runner() Runner {
	return Runner{Paths: f.p, Usage: f.usage, Now: func() time.Time { return f.now }, Log: &f.output}
}

func (f *fixture) run(t *testing.T) RunResult {
	t.Helper()
	res, err := f.runner().Run(context.Background())
	if err != nil {
		t.Fatalf("%v\n%s", err, f.output.String())
	}
	return res
}

// fetched makes the clone pr-loop review would leave for k, at a new head,
// and returns that head.
func (f *fixture) fetched(t *testing.T, k string) string {
	t.Helper()
	slug, n := Split(k)
	clone := filepath.Join(f.root, slug, strconv.Itoa(n), "clone")
	git := func(stdin string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		cmd.Dir = clone
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %q: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(clone, 0o755)
	git("", "init", "--quiet", "--bare")
	tree := git("", "mktree")
	commit := git("", "commit-tree", "-m", k+" "+f.now.String(), tree)
	git("", "update-ref", "refs/pr-review/head", commit)
	os.MkdirAll(filepath.Join(filepath.Dir(clone), commit[:12]), 0o755)
	return commit
}

func (f *fixture) exits(t *testing.T, k string, code int) {
	t.Helper()
	name := "exit." + strings.NewReplacer("/", "_", "#", "_").Replace(k)
	os.WriteFile(filepath.Join(f.state, name), []byte(strconv.Itoa(code)), 0o644)
}

func (f *fixture) logged(t *testing.T) string {
	t.Helper()
	data, _ := os.ReadFile(f.log)
	return string(data)
}

// reviewed is the keys pr-loop was run on, in order, without the head
// each was pinned to.
func (f *fixture) reviewed(t *testing.T) []string {
	t.Helper()
	keys := []string{}
	pinned := regexp.MustCompile(`--head [0-9a-f]{40} `)
	for _, line := range strings.Split(f.logged(t), "\n") {
		if rest, ok := strings.CutPrefix(line, "pr-loop "); ok {
			keys = append(keys, pinned.ReplaceAllString(rest, ""))
		}
	}
	return keys
}

// queue saves entries as the queue, each at headA unless it names a head,
// and lists them on the page at those heads.
func (f *fixture) queue(t *testing.T, entries ...Entry) {
	t.Helper()
	heads := map[string]string{}
	for i := range entries {
		e := &entries[i]
		if e.State == "" {
			e.State = Queued
		}
		if e.Head == "" {
			e.Head = headA
		}
		heads[e.Key] = e.Head
		e.Queued, e.Changed, e.Review, e.Triaged = f.now, f.now, NoExit, NoExit
	}
	listRequests(t, f.p, heads)
	if err := Save(f.p, entries); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) entry(t *testing.T, k string) Entry {
	t.Helper()
	entries, err := Load(f.p)
	if err != nil {
		t.Fatal(err)
	}
	if i := find(entries, k); i >= 0 {
		return entries[i]
	}
	t.Fatalf("%s is not queued", k)
	return Entry{}
}

func (f *fixture) reading(t *testing.T, provider string, used float64, observed time.Time) {
	t.Helper()
	if err := f.usage.Record(usage.Reading{Provider: provider, ObservedAt: observed.Unix(),
		Windows: []usage.Window{{Name: "5h", Used: used, ResetsAt: f.now.Add(2 * time.Hour).Unix()},
			{Name: "7d", Used: used, ResetsAt: f.now.Add(48 * time.Hour).Unix()}}}); err != nil {
		t.Fatal(err)
	}
}

// Each outcome of pr-loop review leaves its state; a review with findings
// or without Codex goes to triage where it is on, in the head's review
// directory, and the triage's quota reading is kept.
func TestRunOutcomes(t *testing.T) {
	f := setUpRunner(t)
	heads := map[string]string{}
	for _, k := range []string{"o/r#1", "o/r#3", "o/r#4", "o/r#5", "o/r#6", "o/r#7", "o/r#8"} {
		heads[k] = f.fetched(t, k)
	}
	f.exits(t, "o/r#3", 3)
	f.exits(t, "o/r#4", 4)
	f.exits(t, "o/r#5", 5)
	f.exits(t, "o/r#6", 3)
	f.exits(t, "o/r#7", 1)
	f.exits(t, "o/r#8", 4)
	f.queue(t, Entry{Key: "o/r#1", Head: heads["o/r#1"], Triage: true}, Entry{Key: "o/r#3", Head: heads["o/r#3"], Triage: true},
		Entry{Key: "o/r#4", Head: heads["o/r#4"], Triage: true}, Entry{Key: "o/r#5", Head: heads["o/r#5"], Triage: true},
		Entry{Key: "o/r#6", Head: heads["o/r#6"]}, Entry{Key: "o/r#7", Head: heads["o/r#7"], Triage: true},
		Entry{Key: "o/r#8", Head: heads["o/r#8"]})

	res := f.run(t)
	if want := []string{"o/r#1", "o/r#3", "o/r#4", "o/r#5", "o/r#6", "o/r#7", "o/r#8"}; !reflect.DeepEqual(res.Ran, want) {
		t.Errorf("ran %q\n%s", res.Ran, f.output.String())
	}
	for k, want := range map[string]struct {
		state           string
		review, triaged int
	}{
		"o/r#1": {Ready, 0, NoExit}, "o/r#3": {Ready, 3, 0}, "o/r#4": {Ready, 4, 0}, "o/r#5": {NeedsOperator, 5, NoExit},
		"o/r#6": {Ready, 3, NoExit}, "o/r#7": {Failed, 1, NoExit}, "o/r#8": {NeedsOperator, 4, NoExit},
	} {
		e := f.entry(t, k)
		if e.State != want.state || e.Review != want.review || e.Triaged != want.triaged || e.Head != heads[k] {
			t.Errorf("%s: %+v, want %+v at %s", k, e, want, heads[k])
		}
	}
	if reason := reason(f.entry(t, "o/r#5")); !strings.Contains(reason, "`pr-loop review --run o/r#5`") {
		t.Errorf("o/r#5's reason: %q", reason)
	}

	log := f.logged(t)
	triages := strings.Split(log, "claude-pr-loop in ")[1:]
	if len(triages) != 2 {
		t.Fatalf("%d triage sessions:\n%s", len(triages), log)
	}
	for i, k := range []string{"o/r#3", "o/r#4"} {
		slug, n := Split(k)
		dir, _ := filepath.EvalSymlinks(filepath.Join(f.root, slug, strconv.Itoa(n), heads[k][:12]))
		if !strings.HasPrefix(triages[i], dir+"\n") {
			t.Errorf("%s's triage ran in %s, not %s", k, strings.SplitN(triages[i], "\n", 2)[0], dir)
		}
		if strings.Contains(triages[i], "--run") && !strings.Contains(triages[i], "never pass --run") {
			t.Errorf("%s's triage allows --run:\n%s", k, triages[i])
		}
		for _, arg := range []string{"arg: -p\n", "arg: dontAsk\n", "arg: Read,Grep,Glob,Bash,Agent\n", "arg: stream-json\n",
			"Bash(pr-loop review settle *),Bash(pr-loop review --check *),Bash(pr-loop review --reviewer subagent *)"} {
			if !strings.Contains(triages[i], arg) {
				t.Errorf("%s's triage lacks %q:\n%s", k, arg, triages[i])
			}
		}
	}
	if !strings.Contains(triages[1], "pr-loop review --reviewer subagent 4") || strings.Contains(triages[0], "--reviewer subagent 3") {
		t.Errorf("only o/r#4's triage reviews with a sub-agent first:\n%s", log)
	}
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "systemd-run ") && !strings.Contains(line, "--user --wait --collect --quiet --slice agent-plans.slice --unit pr-") {
			t.Errorf("unit: %s", line)
		}
	}
	if !strings.Contains(log, `--unit pr-review-o-r\x231 -p MemoryMax=8G -p RuntimeMaxSec=7200 --setenv PATH=`) {
		t.Errorf("review units:\n%s", log)
	}
	// The run ledger's tag, which the review's tools inherit; the triage's
	// launcher sets its own, which the category it is given makes triage.
	for unit, want := range map[string]string{
		`--unit pr-review-o-r\x233 `: " --setenv AGENT_RUN_TAG=category=review;repo=o/r;kind=dispatched;pr=3 ",
		`--unit pr-triage-o-r\x233 `: " --setenv AGENT_RUN_CATEGORY=triage ",
	} {
		seen := false
		for _, line := range strings.Split(log, "\n") {
			if !strings.Contains(line, unit) {
				continue
			}
			seen = true
			if !strings.Contains(line, want) || strings.Contains(want, "CATEGORY") && strings.Contains(line, "AGENT_RUN_TAG") {
				t.Errorf("%s wants %q:\n%s", unit, want, line)
			}
		}
		if !seen {
			t.Errorf("no %s:\n%s", unit, log)
		}
	}
	if r, ok := f.usage.Reading("claude", f.now); !ok || r.Windows[0].Used != 50 {
		t.Errorf("triage's reading %+v, %v", r, ok)
	}
}

// At or past 90% in a fresh reading of a provider the entry would draw on,
// it waits until that window resets, unless the operator ran it anyway; a
// stale reading, or one under the gate, does not hold it.
func TestRunQuotaGate(t *testing.T) {
	f := setUpRunner(t)
	heads := map[string]string{}
	for _, k := range []string{"o/r#1", "o/r#2", "o/r#3"} {
		heads[k] = f.fetched(t, k)
	}
	f.reading(t, "claude", 95, f.now.Add(-time.Hour))
	f.reading(t, "codex", 89, f.now.Add(-time.Hour))
	f.queue(t, Entry{Key: "o/r#1", Triage: true}, Entry{Key: "o/r#2"}, Entry{Key: "o/r#3", Triage: true, Anyway: true})

	res := f.run(t)
	if !reflect.DeepEqual(res.Postponed, []string{"o/r#1"}) || !reflect.DeepEqual(res.Ran, []string{"o/r#2", "o/r#3"}) {
		t.Errorf("result %+v", res)
	}
	e := f.entry(t, "o/r#1")
	if e.State != Postponed || !e.Until.Equal(f.now.Add(48*time.Hour)) {
		t.Errorf("o/r#1: %+v", e)
	}
	if got := f.reviewed(t); !reflect.DeepEqual(got, []string{"review o/r#2", "review o/r#3"}) {
		t.Errorf("reviewed %q", got)
	}
	// "Anyway" passes the gate once.
	if e := f.entry(t, "o/r#3"); e.Anyway {
		t.Errorf("o/r#3 keeps anyway: %+v", e)
	}

	// Still postponed while the window has not reset.
	f.now = f.now.Add(time.Hour)
	if res := f.run(t); len(res.Ran) != 0 || len(res.Postponed) != 0 {
		t.Errorf("result %+v", res)
	}

	// "Run anyway" from the page lifts it past the gate.
	listRequests(t, f.p, map[string]string{"o/r#1": headA})
	putDoc(t, f.p, "o~r~1.json", `{"state": "requested", "anyway": true, "at": "`+f.now.UTC().Format(time.RFC3339)+`"}`)
	if _, err := Dispatch(f.p, f.now, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if res := f.run(t); !reflect.DeepEqual(res.Ran, []string{"o/r#1"}) {
		t.Errorf("result %+v", res)
	}
	if e := f.entry(t, "o/r#1"); e.Anyway {
		t.Errorf("o/r#1 keeps anyway: %+v", e)
	}

	// A reading a day old holds nothing.
	f.reading(t, "codex", 99, f.now.Add(-25*time.Hour))
	f.reading(t, "claude", 99, f.now.Add(-25*time.Hour))
	f.queue(t, Entry{Key: "o/r#2", Triage: true})
	if res := f.run(t); !reflect.DeepEqual(res.Ran, []string{"o/r#2"}) {
		t.Errorf("result %+v", res)
	}

	// Codex's reading holds an entry without triage too.
	f.reading(t, "codex", 90, f.now)
	f.queue(t, Entry{Key: "o/r#3"})
	if res := f.run(t); !reflect.DeepEqual(res.Postponed, []string{"o/r#3"}) || len(res.Ran) != 0 {
		t.Errorf("result %+v", res)
	}

	// Once the window has reset, a postponed entry runs.
	f.now = f.now.Add(49 * time.Hour)
	if res := f.run(t); !reflect.DeepEqual(res.Ran, []string{"o/r#3"}) {
		t.Errorf("result %+v", res)
	}

	// A triage's own reading holds the entries after it.
	f.exits(t, "o/r#1", 3)
	t.Setenv("FAKE_UTIL", "0.95")
	t.Setenv("FAKE_RESET", strconv.FormatInt(f.now.Add(3*time.Hour).Unix(), 10))
	f.queue(t, Entry{Key: "o/r#1", Head: heads["o/r#1"], Triage: true}, Entry{Key: "o/r#2", Triage: true})
	if res := f.run(t); !reflect.DeepEqual(res.Ran, []string{"o/r#1"}) || !reflect.DeepEqual(res.Postponed, []string{"o/r#2"}) {
		t.Errorf("result %+v", res)
	}
	if e := f.entry(t, "o/r#2"); !e.Until.Equal(f.now.Add(3 * time.Hour)) {
		t.Errorf("o/r#2: %+v", e)
	}
}

// With the kill switch on, nothing starts, and a runner stops before its
// next entry once it is turned on.
func TestRunOff(t *testing.T) {
	f := setUpRunner(t)
	f.queue(t, Entry{Key: "o/r#1"}, Entry{Key: "o/r#2"})
	os.WriteFile(f.p.Off(), nil, 0o600)
	if res := f.run(t); !res.Off || len(res.Ran) != 0 {
		t.Errorf("result %+v", res)
	}
	if got := f.reviewed(t); len(got) != 0 {
		t.Errorf("reviewed %q", got)
	}

	os.Remove(f.p.Off())
	t.Setenv("FAKE_OFF", f.p.Off())
	res := f.run(t)
	if !res.Off || !reflect.DeepEqual(res.Ran, []string{"o/r#1"}) {
		t.Errorf("result %+v", res)
	}
	if e := f.entry(t, "o/r#2"); e.State != Queued {
		t.Errorf("o/r#2: %+v", e)
	}
}

// Reviews run one after another, however many runners are started, and an
// entry a stopped runner left running is run again.
func TestRunOneAtATime(t *testing.T) {
	f := setUpRunner(t)
	f.queue(t, Entry{Key: "o/r#1"}, Entry{Key: "o/r#2"}, Entry{Key: "o/r#3", State: Running}, Entry{Key: "o/r#4"})
	var wg sync.WaitGroup
	results := make([]RunResult, 3)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if results[i], err = f.runner().Run(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	log := f.logged(t)
	if strings.Contains(log, "overlap") {
		t.Errorf("two reviews ran at once:\n%s", log)
	}
	ran, busy := 0, 0
	for _, r := range results {
		ran += len(r.Ran)
		if r.Busy {
			busy++
		}
	}
	if ran != 4 || busy == 0 {
		t.Errorf("results %+v", results)
	}
	got := f.reviewed(t)
	if want := []string{"review o/r#1", "review o/r#2", "review o/r#3", "review o/r#4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reviewed %q", got)
	}
}

// A document from the page reaches a command line only as the key it
// names, and only when the export listed it.
func TestRunHostileDocuments(t *testing.T) {
	f := setUpRunner(t)
	putDoc(t, f.p, "o~r~7.json", `{"state": "requested", "triage": false, "key": "SENTINEL/x#1", "head": "--run",
		"argv": ["--run"], "command": "$(SENTINEL)", "STATE": "requested"}`)
	putDoc(t, f.p, "e~x~9.json", `{"state": "requested"}`)
	putDoc(t, f.p, "o~r~7;SENTINEL.json", `{"state": "requested"}`)
	putDoc(t, f.p, "..~SENTINEL~1.json", `{"state": "requested"}`)
	putDoc(t, f.p, "o~r~11.json", `{"state": "requested", "x": "`+strings.Repeat("SENTINEL", maxDoc)+`"}`)
	if _, err := Dispatch(f.p, f.now, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	log := regexp.MustCompile(`--setenv PATH=\S+`).ReplaceAllString(f.logged(t), "")
	if got := f.reviewed(t); !reflect.DeepEqual(got, []string{"review o/r#7"}) {
		t.Errorf("reviewed %q", got)
	}
	for _, s := range []string{"SENTINEL", "--run", "e/x", "o/r#11"} {
		if strings.Contains(log, s) {
			t.Errorf("%q reached a command line:\n%s", s, log)
		}
	}
}

// A review records the head it reviewed; once the page lists a newer one,
// its document says it is stale, and asking again reviews the new head.
func TestRunStaleHead(t *testing.T) {
	f := setUpRunner(t)
	first := f.fetched(t, "o/r#7")
	listRequests(t, f.p, map[string]string{"o/r#7": first})
	putDoc(t, f.p, "o~r~7.json", `{"state": "requested"}`)
	if _, err := Dispatch(f.p, f.now, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if _, err := Dispatch(f.p, f.now, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if d := readDocFile(t, f.p, "o~r~7"); d.State != Ready || d.Stale || d.Head != first {
		t.Errorf("after the review: %+v", d)
	}

	f.now = f.now.Add(time.Minute)
	second := f.fetched(t, "o/r#7")
	listRequests(t, f.p, map[string]string{"o/r#7": second})
	if _, err := Dispatch(f.p, f.now, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if d := readDocFile(t, f.p, "o~r~7"); d.State != Ready || !d.Stale || d.Head != first {
		t.Errorf("after a push: %+v", d)
	}
	putDoc(t, f.p, "o@r@7.json", `{"state": "requested", "at": "`+f.now.UTC().Format(time.RFC3339)+`"}`)
	if _, err := Dispatch(f.p, f.now, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if d := readDocFile(t, f.p, "o~r~7"); d.State != Queued || d.Stale || d.Head != second {
		t.Errorf("asked again: %+v", d)
	}
	f.run(t)
	if e := f.entry(t, "o/r#7"); e.State != Ready || e.Head != second {
		t.Errorf("reviewed again: %+v", e)
	}
}

// A review is pinned to the head the page listed: one GitHub has moved past
// is not reviewed, and the entry is stale.
func TestRunPinnedHead(t *testing.T) {
	f := setUpRunner(t)
	f.fetched(t, "o/r#7")
	f.exits(t, "o/r#7", 6)
	f.queue(t, Entry{Key: "o/r#7", Head: headA, Triage: true})
	f.run(t)
	if !strings.Contains(f.logged(t), "pr-loop review --head "+headA+" o/r#7\n") {
		t.Errorf("not pinned:\n%s", f.logged(t))
	}
	if strings.Contains(f.logged(t), "claude-pr-loop") {
		t.Error("triaged a moved head")
	}
	e := f.entry(t, "o/r#7")
	if e.State != Stale || e.Review != 6 || e.Head != headA || !Final(e.State) || !strings.Contains(reason(e), "ask again") {
		t.Errorf("o/r#7: %+v, %q", e, reason(e))
	}
}

// Before it runs an entry, the runner checks it against the page's listing
// again: queue.org naming a request not listed, or another head, runs
// nothing, anyway or not.
func TestRunRechecksListing(t *testing.T) {
	f := setUpRunner(t)
	listRequests(t, f.p, map[string]string{"o/r#7": headA})
	if err := Save(f.p, []Entry{
		{Key: "x/y#20", State: Queued, Head: headA, Anyway: true, Queued: f.now, Changed: f.now, Review: NoExit, Triaged: NoExit},
		{Key: "o/r#7", State: Queued, Head: headB, Queued: f.now, Changed: f.now, Review: NoExit, Triaged: NoExit},
	}); err != nil {
		t.Fatal(err)
	}
	res := f.run(t)
	if got := f.reviewed(t); len(got) != 0 || len(res.Ran) != 0 {
		t.Errorf("reviewed %q, result %+v", got, res)
	}
	for _, k := range []string{"x/y#20", "o/r#7"} {
		if e := f.entry(t, k); e.State != Stale || e.Review != NoExit || e.Anyway {
			t.Errorf("%s: %+v", k, e)
		}
	}
}

// Stopping the runner stops the review it started; started again, it runs
// that review again, saying so, once its units are gone, and not while one
// is still active.
func TestRunStopped(t *testing.T) {
	f := setUpRunner(t)
	t.Setenv("FAKE_SLEEP", "2")
	f.queue(t, Entry{Key: "o/r#7"}, Entry{Key: "o/r#8"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	if _, err := f.runner().Run(ctx); err == nil {
		t.Error("a stopped run returned no error")
	}
	if !strings.Contains(f.logged(t), `systemctl --user stop pr-review-o-r\x237.service pr-triage-o-r\x237.service`) {
		t.Errorf("the review's units were not stopped:\n%s", f.logged(t))
	}
	if e := f.entry(t, "o/r#7"); e.State != Running {
		t.Errorf("o/r#7 after the stop: %+v", e)
	}
	os.Remove(filepath.Join(f.state, "busy"))

	t.Setenv("FAKE_SLEEP", "0")
	os.WriteFile(filepath.Join(f.state, "active"), nil, 0o600)
	res := f.run(t)
	if len(res.Ran) != 0 || res.Waiting != "o/r#7" {
		t.Errorf("with its unit still active: %+v", res)
	}
	if e := f.entry(t, "o/r#7"); e.State != Running {
		t.Errorf("o/r#7: %+v", e)
	}

	os.Remove(filepath.Join(f.state, "active"))
	var restarted Entry
	r := f.runner()
	res, err := r.Run(context.Background())
	if err != nil || !reflect.DeepEqual(res.Ran, []string{"o/r#7", "o/r#8"}) {
		t.Errorf("result %+v (%v)", res, err)
	}
	if !strings.Contains(f.output.String(), "o/r#7 runs again: its last run was stopped") {
		t.Errorf("no word of the rerun:\n%s", f.output.String())
	}
	if restarted = f.entry(t, "o/r#7"); restarted.State != Ready || restarted.Restarted {
		t.Errorf("o/r#7: %+v", restarted)
	}
	if reason(Entry{Key: "o/r#7", State: Running, Restarted: true}) == "" {
		t.Error("a rerun's entry does not say so")
	}
}

func TestUnitName(t *testing.T) {
	for k, want := range map[string]string{
		"o/r#7":                `o-r\x237`,
		"wesnel/nix-config#41": `wesnel-nix\x2dconfig\x2341`,
		".o/r.x#1":             `\x2eo-r.x\x231`,
	} {
		if got := unitName(k); got != want {
			t.Errorf("%s: %s, want %s", k, got, want)
		}
	}
	if got := unitName(strings.Repeat("a", 100) + "/" + strings.Repeat("-", 100) + "#1"); len(got) > 200 {
		t.Errorf("%d characters", len(got))
	}
}
