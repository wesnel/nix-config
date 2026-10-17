package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	now   = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	headA = strings.Repeat("a", 40)
	headB = strings.Repeat("b", 40)
)

// setUp is a state directory whose last export listed o/r#7 at headA and
// o/r#8, o/r#10 and o/r#11 at headB.
func setUp(t *testing.T) Paths {
	t.Helper()
	p := Paths{Dir: filepath.Join(t.TempDir(), "agent-plans")}
	listRequests(t, p, map[string]string{"o/r#7": headA, "o/r#8": headB, "o/r#10": headB, "o/r#11": headB})
	if err := os.MkdirAll(filepath.Join(p.Out(), "dispatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func listRequests(t *testing.T, p Paths, heads map[string]string) {
	t.Helper()
	var requests []Request
	for k, h := range heads {
		slug, n := Split(k)
		requests = append(requests, Request{Repo: slug, Number: n, Head: h})
	}
	if err := WriteListed(p, requests); err != nil {
		t.Fatal(err)
	}
}

func putDoc(t *testing.T, p Paths, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.Out(), "dispatch", name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readDocFile reads document id as an ArtifactData read lays it out,
// "~" spelled "@" in its file's name.
func readDocFile(t *testing.T, p Paths, id string) Doc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.Out(), "dispatch", strings.ReplaceAll(id, "~", "@")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var d Doc
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		t.Fatalf("%s: %v: %s", id, err, data)
	}
	return d
}

func dispatch(t *testing.T, p Paths, at time.Time) (Result, int) {
	t.Helper()
	starts := 0
	res, err := Dispatch(p, at, func() error { starts++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return res, starts
}

func TestDispatch(t *testing.T) {
	p := setUp(t)
	putDoc(t, p, "o~r~7.json", `{"state": "requested", "at": "2026-10-16T11:59:00Z"}`)
	putDoc(t, p, "o@r@99.json", `{"state": "requested"}`)
	res, starts := dispatch(t, p, now)
	if !reflect.DeepEqual(res.Queued, []string{"o/r#7"}) || !res.Started || starts != 1 || res.Off {
		t.Errorf("result %+v, %d starts", res, starts)
	}
	// The documents written back, for the session to put on the page.
	if !reflect.DeepEqual(res.Written, []string{"o~r~7", "o~r~99"}) {
		t.Errorf("written %v", res.Written)
	}
	os.Remove(filepath.Join(p.Out(), "dispatch", "o@r@99.json"))
	entries, err := Load(p)
	want := []Entry{{Key: "o/r#7", State: Queued, Head: headA, Triage: true, Queued: now, Changed: now, Review: NoExit, Triaged: NoExit}}
	if err != nil || !reflect.DeepEqual(entries, want) {
		t.Errorf("queue %+v (%v)", entries, err)
	}
	if d := readDocFile(t, p, "o~r~7"); d != (Doc{State: Queued, Triage: true, At: "2026-10-16T12:00:00Z", Head: headA}) {
		t.Errorf("doc %+v", d)
	}

	// Asked again while queued, nothing changes, but the runner is started
	// again in case it is not running.
	putDoc(t, p, "o~r~7.json", `{"state": "requested", "triage": false}`)
	res, starts = dispatch(t, p, now.Add(time.Minute))
	if len(res.Queued) != 0 || starts != 1 {
		t.Errorf("result %+v, %d starts", res, starts)
	}
	if again, _ := Load(p); !reflect.DeepEqual(again, want) {
		t.Errorf("queue %+v", again)
	}
}

// Nothing a document holds reaches the queue but its state, triage and
// anyway, and only for a key the export listed, named by a well-formed
// document ID in a regular file of a few KiB.
func TestDispatchHostile(t *testing.T) {
	p := setUp(t)
	putDoc(t, p, "o~r~7.json", `{"state": "requested", "triage": false, "repo": "evil/x", "number": 9,
		"head": "`+headB+`", "command": "SENTINEL; rm -rf ~", "key": "evil/x#9"}`)
	putDoc(t, p, "e~x~9.json", `{"state": "requested"}`)
	for _, name := range []string{"..~..~1.json", ".~r~1.json", "o~r~07.json", "o~r~7;id.json", "o~r~7.json.json",
		"o~r~7", ".json", "o~r~7 .json", "o~r.json", "o~r~12345678901.json"} {
		putDoc(t, p, name, `{"state": "requested"}`)
	}
	os.Mkdir(filepath.Join(p.Out(), "dispatch", "o~r~9.json"), 0o700)
	outside := filepath.Join(t.TempDir(), "outside.json")
	os.WriteFile(outside, []byte(`{"state": "requested"}`), 0o600)
	if err := os.Symlink(outside, filepath.Join(p.Out(), "dispatch", "o~r~8.json")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(p.Out(), "dispatch", "o~r~10.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	putDoc(t, p, "o~r~11.json", `{"state": "requested", "pad": "`+strings.Repeat("x", maxDoc)+`"}`)

	res, _ := dispatch(t, p, now)
	if !reflect.DeepEqual(res.Queued, []string{"o/r#7"}) {
		t.Errorf("queued %q", res.Queued)
	}
	refused := map[string]bool{}
	for _, id := range res.Refused {
		refused[id] = true
	}
	if !reflect.DeepEqual(refused, map[string]bool{"e~x~9": true, "o~r~8": true, "o~r~10": true, "o~r~11": true}) {
		t.Errorf("refused %q", res.Refused)
	}
	if d := readDocFile(t, p, "e~x~9"); d.State != "refused" || d.Reason != notListed {
		t.Errorf("e~x~9: %+v", d)
	}
	for _, id := range []string{"o~r~8", "o~r~10", "o~r~11"} {
		if d := readDocFile(t, p, id); d.State != "refused" || d.Reason != unreadable {
			t.Errorf("%s: %+v", id, d)
		}
	}
	if data, _ := os.ReadFile(outside); string(data) != `{"state": "requested"}` {
		t.Errorf("wrote through the link: %s", data)
	}
	if res.Ignored != 11 {
		t.Errorf("ignored %d", res.Ignored)
	}

	entries, err := Load(p)
	if err != nil || len(entries) != 1 || entries[0].Key != "o/r#7" || entries[0].Head != headA || entries[0].Triage {
		t.Errorf("queue %+v (%v)", entries, err)
	}
	queue, _ := os.ReadFile(p.Queue())
	for _, s := range []string{"SENTINEL", "evil", headB} {
		if strings.Contains(string(queue), s) {
			t.Errorf("queue.org holds %q:\n%s", s, queue)
		}
	}
	files, _ := os.ReadDir(filepath.Join(p.Out(), "dispatch"))
	var names []string
	for _, f := range files {
		names = append(names, f.Name())
	}
	want := []string{"e@x@9.json", "o@r@10.json", "o@r@11.json", "o@r@7.json", "o@r@8.json", "o~r~9.json"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("left %q", names)
	}
	for _, id := range []string{"o~r~7", "e~x~9"} {
		data, _ := os.ReadFile(filepath.Join(p.Out(), "dispatch", strings.ReplaceAll(id, "~", "@")+".json"))
		if strings.Contains(string(data), "SENTINEL") || strings.Contains(string(data), "evil") {
			t.Errorf("%s: %s", id, data)
		}
	}
}

// An ArtifactData read spells "~" in a document ID as "@" in its file's
// name. Either spelling is read, all of one or the other; two files for one
// document are refused; and what dispatch writes back is spelled as a read
// spells it, leaving one file per document.
func TestDispatchOutDirSpelling(t *testing.T) {
	p := setUp(t)
	listRequests(t, p, map[string]string{"example-org/widgets#7": headA, "o/r#8": headB, "o/r#10": headB})
	putDoc(t, p, "example-org@widgets@7.json", `{"state": "requested"}`)
	putDoc(t, p, "o@r@8.json", `{"state": "requested"}`)
	putDoc(t, p, "o~r~8.json", `{"state": "requested"}`)
	putDoc(t, p, "o~r@10.json", `{"state": "requested"}`)
	putDoc(t, p, "o@r~10.json", `{"state": "requested"}`)
	putDoc(t, p, "o@@r@10.json", `{"state": "requested"}`)

	res, _ := dispatch(t, p, now)
	if !reflect.DeepEqual(res.Queued, []string{"example-org/widgets#7"}) || !reflect.DeepEqual(res.Refused, []string{"o~r~8"}) ||
		res.Ignored != 3 {
		t.Errorf("result %+v", res)
	}
	if d := readDocFile(t, p, "example-org~widgets~7"); d.State != Queued || d.Head != headA {
		t.Errorf("example-org~widgets~7: %+v", d)
	}
	if d := readDocFile(t, p, "o~r~8"); d.State != "refused" || d.Reason != ambiguous {
		t.Errorf("o~r~8: %+v", d)
	}
	files, _ := os.ReadDir(filepath.Join(p.Out(), "dispatch"))
	var names []string
	for _, f := range files {
		names = append(names, f.Name())
	}
	if want := []string{"example-org@widgets@7.json", "o@r@8.json"}; !reflect.DeepEqual(names, want) {
		t.Errorf("left %q", names)
	}
}

func TestDispatchLinkedDirectory(t *testing.T) {
	p := setUp(t)
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "o~r~7.json"), []byte(`{"state": "requested"}`), 0o600)
	os.RemoveAll(filepath.Join(p.Out(), "dispatch"))
	os.Symlink(elsewhere, filepath.Join(p.Out(), "dispatch"))
	if _, err := Dispatch(p, now, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err %v", err)
	}
	if entries, _ := Load(p); len(entries) != 0 {
		t.Errorf("queue %+v", entries)
	}
}

func TestDispatchWithoutExport(t *testing.T) {
	p := Paths{Dir: t.TempDir()}
	if _, err := Dispatch(p, now, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "plan-dash export") {
		t.Errorf("err %v", err)
	}
}

// An entry that has finished is queued again when asked, at the head now
// listed; until then its document says when the page lists another head.
func TestDispatchAgain(t *testing.T) {
	p := setUp(t)
	if err := Save(p, []Entry{
		{Key: "o/r#7", State: Ready, Head: headA, Triage: true, Queued: now, Changed: now, Review: 3, Triaged: 0},
		{Key: "o/r#8", State: Postponed, Head: headB, Triage: true, Queued: now, Changed: now, Until: now.Add(time.Hour),
			Review: NoExit, Triaged: NoExit},
		{Key: "x/y#1", State: Failed, Head: headA, Queued: now, Changed: now, Review: 1, Triaged: NoExit},
	}); err != nil {
		t.Fatal(err)
	}
	putDoc(t, p, "x@y@1.json", `{"state": "failed"}`)
	listRequests(t, p, map[string]string{"o/r#7": headB, "o/r#8": headB})

	res, starts := dispatch(t, p, now)
	if starts != 0 || len(res.Queued) != 0 {
		t.Errorf("result %+v, %d starts with nothing to run", res, starts)
	}
	if d := readDocFile(t, p, "o~r~7"); d.State != Ready || !d.Stale || d.Head != headA {
		t.Errorf("o~r~7: %+v", d)
	}
	if d := readDocFile(t, p, "o~r~8"); d.State != Postponed || d.Stale || d.Until != "2026-10-16T13:00:00Z" || d.Reason == "" {
		t.Errorf("o~r~8: %+v", d)
	}
	if _, err := os.Stat(filepath.Join(p.Out(), "dispatch", "x@y@1.json")); err == nil {
		t.Error("kept the document of a finished review no longer listed")
	}

	// A request no newer than the entry's last dispatch, or with no time,
	// is the one already acted on, which the page has not replaced yet.
	for _, at := range []string{`"at": "2026-10-16T11:59:00Z"`, `"at": "2026-10-16T12:00:00Z"`, `"at": "yesterday"`, `"x": 1`} {
		putDoc(t, p, "o@r@7.json", `{"state": "requested", `+at+`}`)
		putDoc(t, p, "o@r@8.json", `{"state": "requested", "anyway": true, `+at+`}`)
		if res, starts = dispatch(t, p, now.Add(time.Minute)); len(res.Queued) != 0 || len(res.Anyway) != 0 || starts != 0 {
			t.Errorf("%s: result %+v, %d starts", at, res, starts)
		}
	}

	putDoc(t, p, "o@r@7.json", `{"state": "requested", "at": "2026-10-16T12:01:00Z"}`)
	putDoc(t, p, "o@r@8.json", `{"state": "requested", "anyway": true, "at": "2026-10-16T12:01:00Z"}`)
	res, starts = dispatch(t, p, now.Add(time.Minute))
	if !reflect.DeepEqual(res.Queued, []string{"o/r#7"}) || !reflect.DeepEqual(res.Anyway, []string{"o/r#8"}) || starts != 1 {
		t.Errorf("result %+v, %d starts", res, starts)
	}
	entries, _ := Load(p)
	if len(entries) != 2 || entries[0].State != Queued || entries[0].Head != headB || entries[0].Review != NoExit ||
		entries[0].Anyway || entries[1].State != Queued || !entries[1].Anyway || !entries[1].Until.IsZero() {
		t.Errorf("queue %+v", entries)
	}
	if d := readDocFile(t, p, "o~r~7"); d.State != Queued || d.Stale {
		t.Errorf("o~r~7: %+v", d)
	}

	// The same documents, read again before the page replaces them, queue
	// nothing more.
	putDoc(t, p, "o@r@7.json", `{"state": "requested", "at": "2026-10-16T12:01:00Z"}`)
	if res, _ = dispatch(t, p, now.Add(2*time.Minute)); len(res.Queued) != 0 {
		t.Errorf("result %+v", res)
	}
}

// "Run anyway" lifts only an entry the quota gate postponed: a new or
// finished entry asked for with it is queued behind the gate, and no
// document carries it back to the page.
func TestDispatchAnywayOnlyPostponed(t *testing.T) {
	p := setUp(t)
	if err := Save(p, []Entry{{Key: "o/r#8", State: Ready, Head: headB, Queued: now, Changed: now, Review: 0, Triaged: NoExit}}); err != nil {
		t.Fatal(err)
	}
	putDoc(t, p, "o@r@7.json", `{"state": "requested", "anyway": true}`)
	putDoc(t, p, "o@r@8.json", `{"state": "requested", "anyway": true, "at": "2026-10-16T12:05:00Z"}`)
	res, _ := dispatch(t, p, now.Add(10*time.Minute))
	if len(res.Queued) != 2 || len(res.Anyway) != 0 {
		t.Errorf("result %+v", res)
	}
	entries, _ := Load(p)
	for _, e := range entries {
		if e.Anyway {
			t.Errorf("%s queued anyway", e.Key)
		}
	}
	for _, id := range []string{"o@r@7", "o@r@8"} {
		data, _ := os.ReadFile(filepath.Join(p.Out(), "dispatch", id+".json"))
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil || doc["anyway"] != nil {
			t.Errorf("%s: %s", id, data)
		}
	}
}

// The dispatch directory is opened once: swapped for a link after that,
// nothing is read, removed or written through the link.
func TestDispatchDirectorySwapped(t *testing.T) {
	p := setUp(t)
	putDoc(t, p, "o@r@7.json", `{"state": "requested"}`)
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "keep.txt"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(elsewhere, "o@r@8.json"), []byte(`{"state": "requested"}`), 0o600)
	dir := filepath.Join(p.Out(), "dispatch")
	moved := filepath.Join(p.Out(), "moved")
	opened = func() {
		os.Rename(dir, moved)
		os.Symlink(elsewhere, dir)
	}
	t.Cleanup(func() { opened = func() {} })
	res, _ := dispatch(t, p, now)
	if !reflect.DeepEqual(res.Queued, []string{"o/r#7"}) {
		t.Errorf("result %+v", res)
	}
	files, _ := os.ReadDir(elsewhere)
	var names []string
	for _, f := range files {
		names = append(names, f.Name())
	}
	if !reflect.DeepEqual(names, []string{"keep.txt", "o@r@8.json"}) {
		t.Errorf("through the link: %q", names)
	}
	if _, err := os.Stat(filepath.Join(moved, "o@r@7.json")); err != nil {
		t.Errorf("the document went elsewhere: %v", err)
	}
}

// While the kill switch is on, requests are still queued, but nothing is
// started.
func TestDispatchOff(t *testing.T) {
	p := setUp(t)
	os.WriteFile(p.Off(), nil, 0o600)
	putDoc(t, p, "o~r~7.json", `{"state": "requested"}`)
	res, starts := dispatch(t, p, now)
	if !res.Off || res.Started || starts != 0 || len(res.Queued) != 1 {
		t.Errorf("result %+v, %d starts", res, starts)
	}
}

// A queue.org that does not parse is never acted on, nor rewritten.
func TestLoadRefuses(t *testing.T) {
	entry := "* QUEUED o/r#7\n:PROPERTIES:\n:HEAD: " + headA + "\n:TRIAGE: t\n:END:\n"
	for name, text := range map[string]string{
		"unknown state":    strings.Replace(entry, "QUEUED", "DONE", 1),
		"path key":         strings.Replace(entry, "o/r#7", "../r#7", 1),
		"unknown property": strings.Replace(entry, ":TRIAGE: t", ":COMMAND: rm", 1),
		"bad head":         strings.Replace(entry, headA, "HEAD", 1),
		"bad flag":         strings.Replace(entry, ":TRIAGE: t", ":TRIAGE: yes", 1),
		"no drawer":        "* QUEUED o/r#7\n",
		"stray text":       entry + "some text\n",
		"twice":            entry + entry,
	} {
		t.Run(name, func(t *testing.T) {
			p := setUp(t)
			os.WriteFile(p.Queue(), []byte(text), 0o600)
			putDoc(t, p, "o~r~7.json", `{"state": "requested"}`)
			if _, err := Dispatch(p, now, func() error { t.Error("started"); return nil }); err == nil {
				t.Error("no error")
			}
			if data, _ := os.ReadFile(p.Queue()); string(data) != text {
				t.Errorf("rewrote it: %s", data)
			}
		})
	}
}

func TestSaveLoad(t *testing.T) {
	p := Paths{Dir: t.TempDir()}
	in := []Entry{
		{Key: "o/r#7", State: NeedsOperator, Head: headA, Triage: true, Anyway: true, Queued: now, Changed: now.Add(time.Hour),
			Review: 5, Triaged: NoExit},
		{Key: "o.x/r_y-z#1", State: Postponed, Queued: now, Changed: now, Until: now.Add(2 * time.Hour), Review: NoExit, Triaged: NoExit},
	}
	if err := Save(p, in); err != nil {
		t.Fatal(err)
	}
	if out, err := Load(p); err != nil || !reflect.DeepEqual(out, in) {
		t.Errorf("got %+v (%v)", out, err)
	}
	if err := Save(p, []Entry{{Key: "o/r#7\n* READY x/y#1", State: Queued}}); err == nil {
		t.Error("saved a key with a newline")
	}
	if got := DocID("o.x/r_y-z#12"); got != "o.x~r_y-z~12" {
		t.Errorf("DocID %q", got)
	}
}
