package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/queue"
)

var (
	day = time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local)
	now = day.Add(time.Hour)
)

// under puts text after the line heading in file.
func under(t *testing.T, file, heading, text string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), heading+"\n", heading+"\n"+text, 1)
	if edited == string(data) {
		t.Fatalf("%s has no %q", file, heading)
	}
	os.WriteFile(file, []byte(edited), 0o644)
}

// answerFixture is a plans directory holding box's plan, whose Q1 has
// options A to C, Q2 only A, and Q3, written without a recommendation, B
// and C; and mac's plan with a question of its own. The last export of
// them is listed, and out/answers is ready for the page's documents.
func answerFixture(t *testing.T) (queue.Paths, plans.Config, plans.Plan, plans.Plan) {
	t.Helper()
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	made, err := c.New(plans.NewOptions{Title: "Loop", Slug: "loop", Goal: "Run the loop."}, day)
	if err != nil {
		t.Fatal(err)
	}
	own, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := own.Ask(plans.AskOptions{Question: "Keep the flag?", Recommendation: "no, drop it",
		Alternatives: []string{"keep it :: costs a flag", "rename it"}}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := own.Ask(plans.AskOptions{Question: "Which name?", Recommendation: "loop"}, day); err != nil {
		t.Fatal(err)
	}
	under(t, own.File(), "** Questions", "*** ASK Which colour?\n:PROPERTIES:\n:ID: Q3\n:END:\n- red\n- blue\n")

	mac := plans.Config{Dir: c.Dir, Host: "mac"}
	made, err = mac.New(plans.NewOptions{Title: "Theirs", Slug: "theirs", Goal: "Their goal."}, day)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := mac.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := theirs.Ask(plans.AskOptions{Question: "Ship it?", Recommendation: "yes"}, day); err != nil {
		t.Fatal(err)
	}

	p := queue.Paths{Dir: filepath.Join(t.TempDir(), "agent-plans")}
	exported, _, err := export.Plans(c, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteQuestions(p, c.Host, exported); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.Out(), "answers"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p, c, own, theirs
}

// putAnswer writes a page's document, named as an ArtifactData read names
// it: "~" spelled "@".
func putAnswer(t *testing.T, p queue.Paths, id, body string) {
	t.Helper()
	name := strings.ReplaceAll(id, "~", "@") + ".json"
	if err := os.WriteFile(filepath.Join(p.Out(), "answers", name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAnswer(t *testing.T, p queue.Paths, id string) AnswerDoc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.Out(), "answers", strings.ReplaceAll(id, "~", "@")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var d AnswerDoc
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		t.Fatalf("%s: %v: %s", id, err, data)
	}
	return d
}

func answers(t *testing.T, p queue.Paths, c plans.Config, at time.Time) AnswerResult {
	t.Helper()
	res, err := Answer(p, c, at)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func decisions(t *testing.T, p plans.Plan) map[string]string {
	t.Helper()
	ds, err := p.Decisions(true)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range ds {
		out[d.ID] = d.Answer
	}
	return out
}

func TestAnswer(t *testing.T) {
	p, c, own, _ := answerFixture(t)
	stamp := now.UTC().Format(time.RFC3339)
	putAnswer(t, p, own.Name+"~Q1", `{"choice": "B", "at": "2026-10-17T09:30:00Z"}`)
	// Q3 has no recommendation, so its first option is B: a choice maps to
	// its answer by key, never by its place in the list.
	putAnswer(t, p, own.Name+"~Q3", `{"choice": "B"}`)
	putAnswer(t, p, own.Name+"~Q2", `{"choice": "other", "text": "  call it ring  "}`)
	res := answers(t, p, c, now)
	want := []string{own.Name + "~Q1", own.Name + "~Q2", own.Name + "~Q3"}
	if !reflect.DeepEqual(res.Recorded, want) || len(res.Settled)+len(res.Refused)+res.Ignored != 0 {
		t.Errorf("result %+v", res)
	}
	if got := decisions(t, own); !reflect.DeepEqual(got, map[string]string{"Q1": "keep it", "Q2": "call it ring", "Q3": "red"}) {
		t.Errorf("decisions %v", got)
	}
	events, err := own.Events()
	if err != nil {
		t.Fatal(err)
	}
	phone := 0
	for _, e := range events {
		if e.Kind == "decided" && e.Role == "phone" {
			phone++
		}
	}
	if phone != 3 {
		t.Errorf("%d decided events by the phone: %+v", phone, events)
	}
	for id, choice := range map[string]string{"Q1": "B", "Q2": "other", "Q3": "B"} {
		if d := readAnswer(t, p, own.Name+"~"+id); d != (AnswerDoc{Schema: 1, Choice: choice, State: "recorded", At: stamp}) {
			t.Errorf("%s: doc %+v", id, d)
		}
	}

	// The documents written back are not asked again, and a question asked
	// again once settled counts as settled, not refused.
	res = answers(t, p, c, now.Add(time.Minute))
	if len(res.Recorded)+len(res.Settled)+len(res.Refused) != 0 {
		t.Errorf("second run %+v", res)
	}
	putAnswer(t, p, own.Name+"~Q1", `{"choice": "C"}`)
	res = answers(t, p, c, now.Add(2*time.Minute))
	if !reflect.DeepEqual(res.Settled, []string{own.Name + "~Q1"}) || len(res.Recorded) != 0 {
		t.Errorf("asked again %+v", res)
	}
	if got := decisions(t, own)["Q1"]; got != "keep it" {
		t.Errorf("Q1 changed to %q", got)
	}
}

// Nothing a document holds reaches a plan but a choice of an option the
// last export listed for a question of this host's, or one line of the
// operator's own; every other document is refused or removed.
func TestAnswerHostile(t *testing.T) {
	p, c, own, theirs := answerFixture(t)
	refused := map[string]string{
		// By place, not key.
		own.Name + "~Q1": `{"choice": "1"}`,
		// No such option.
		own.Name + "~Q2": `{"choice": "B", "answer": "SENTINEL-answer", "text": "SENTINEL-text"}`,
		// Another host's plan: answered there.
		theirs.Name + "~Q1": `{"choice": "A"}`,
		// Not a question the export listed.
		own.Name + "~Q7": `{"choice": "A"}`,
		// A plan the export did not list.
		"2026-10-17-ghost.box~Q1": `{"choice": "A"}`,
	}
	for id, body := range refused {
		putAnswer(t, p, id, body)
	}
	res := answers(t, p, c, now)
	var want []string
	for id := range refused {
		want = append(want, id)
	}
	slices.Sort(want)
	if !reflect.DeepEqual(res.Refused, want) || len(res.Recorded)+len(res.Settled) != 0 {
		t.Errorf("result %+v, want refused %v", res, want)
	}
	for id := range refused {
		if d := readAnswer(t, p, id); d.State != "refused" || d.Reason == "" || strings.Contains(d.Reason, "SENTINEL") {
			t.Errorf("%s: doc %+v", id, d)
		}
	}
	if got := decisions(t, own); len(got) != 0 {
		t.Errorf("decisions %v", got)
	}
	if got := decisions(t, theirs); len(got) != 0 {
		t.Errorf("their decisions %v", got)
	}

	// "Other" takes one line of text, and nothing else.
	for _, body := range []string{`{"choice": "other"}`, `{"choice": "other", "text": "   "}`,
		`{"choice": "other", "text": "two\nlines"}`, `{"choice": "other", "text": "line\rbreak"}`,
		`{"choice": "other", "text": "hidden‮right"}`, `{"choice": "other", "text": "` + strings.Repeat("x", 1001) + `"}`,
		`{"choice": "OTHER", "text": "shouting"}`, `{"choice": "a"}`, `{"choice": ""}`, `{}`, `{"choice": 2}`,
		`["A"]`, `not json`, `{"choice": "other", "text": "` + strings.Repeat("y", 17<<10) + `"}`} {
		putAnswer(t, p, own.Name+"~Q1", body)
		res := answers(t, p, c, now)
		if !reflect.DeepEqual(res.Refused, []string{own.Name + "~Q1"}) || len(res.Recorded) != 0 {
			t.Errorf("%.80s: result %+v", body, res)
		}
	}
	if got := decisions(t, own); len(got) != 0 {
		t.Errorf("decisions %v", got)
	}

	dir := filepath.Join(p.Out(), "answers")
	// Names that spell no document are removed; a directory is left.
	for _, name := range []string{"..@Q1.json", own.Name + "@Q1;id.json", own.Name + "@Q1", own.Name + ".json",
		own.Name + "@Qx.json", own.Name + "@Q1@Q2.json", ".json", own.Name + "@../Q1.json"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), []byte(`{"choice": "A"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, own.Name+"@Q4.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A link is not followed; two spellings of one document are refused.
	outside := filepath.Join(t.TempDir(), "outside.json")
	os.WriteFile(outside, []byte(`{"choice": "A"}`), 0o600)
	os.Symlink(outside, filepath.Join(dir, own.Name+"@Q3.json"))
	os.Remove(filepath.Join(dir, own.Name+"@Q1.json"))
	putAnswer(t, p, own.Name+"~Q1", `{"choice": "A"}`)
	os.WriteFile(filepath.Join(dir, own.Name+"~Q1.json"), []byte(`{"choice": "A"}`), 0o600)
	// A listing under out, which the session can write, is not read.
	os.WriteFile(filepath.Join(p.Out(), "questions.json"), []byte(`{"plans": {}, "questions": []}`), 0o600)
	res = answers(t, p, c, now)
	if !reflect.DeepEqual(res.Refused, []string{own.Name + "~Q1", own.Name + "~Q3"}) || len(res.Recorded) != 0 ||
		res.Ignored != 9 {
		t.Errorf("result %+v", res)
	}
	if got := decisions(t, own); len(got) != 0 {
		t.Errorf("decisions %v", got)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	wantNames := []string{own.Name + "@Q1.json", own.Name + "@Q2.json", own.Name + "@Q3.json", own.Name + "@Q4.json",
		theirs.Name + "@Q1.json",
		"2026-10-17-ghost.box@Q1.json", own.Name + "@Q7.json"}
	slices.Sort(wantNames)
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("left %v, want %v", names, wantNames)
	}
	if data, _ := os.ReadFile(outside); string(data) != `{"choice": "A"}` {
		t.Errorf("the link's target changed: %s", data)
	}
}

// An answer of the operator's own lands in an Org heading of the log and a
// property, so it can form neither a link nor the heading's tags.
func TestAnswerOrgShape(t *testing.T) {
	p, c, own, _ := answerFixture(t)
	for id, text := range map[string]string{
		"Q1": `see [[elisp:(delete-file \"x\")][docs]] :urgent:ops:`,
		"Q2": `ring [[https://example.com]]`,
		"Q3": `keep: it, as is :x:`,
	} {
		putAnswer(t, p, own.Name+"~"+id, `{"choice": "other", "text": "`+text+`"}`)
	}
	res := answers(t, p, c, now)
	if len(res.Recorded) != 3 {
		t.Fatalf("result %+v", res)
	}
	want := map[string]string{
		"Q1": `see [ [elisp:(delete-file "x")][docs] ] :urgent:ops:.`,
		"Q2": `ring [ [https://example.com] ]`,
		"Q3": `keep: it, as is :x:.`,
	}
	if got := decisions(t, own); !reflect.DeepEqual(got, want) {
		t.Errorf("decisions %q, want %q", got, want)
	}
}

// Without a listing, nothing is answered.
func TestAnswerUnlisted(t *testing.T) {
	p, c, own, _ := answerFixture(t)
	os.Remove(QuestionsFile(p))
	putAnswer(t, p, own.Name+"~Q1", `{"choice": "A"}`)
	if _, err := Answer(p, c, now); err == nil || !strings.Contains(err.Error(), "plan-dash export") {
		t.Errorf("err %v", err)
	}
	if got := decisions(t, own); len(got) != 0 {
		t.Errorf("decisions %v", got)
	}
}
