package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// Request is a review request as the export listed it on the page.
type Request struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Head   string `json:"head"`
}

// WriteListed records requests as the ones the page lists, for Dispatch to
// check the page's requests against; those without a key's or a head's shape
// are left out.
func WriteListed(p Paths, requests []Request) error {
	kept := []Request{}
	for _, r := range requests {
		if key.MatchString(fmt.Sprintf("%s#%d", r.Repo, r.Number)) && fullHead.MatchString(r.Head) {
			kept = append(kept, r)
		}
	}
	data, err := json.Marshal(struct {
		Requests []Request `json:"requests"`
	}{kept})
	if err != nil {
		return err
	}
	return writeFile(p.Listed(), data)
}

// listed is the last export's requests, by key, each with its head.
func listed(p Paths) (map[string]string, error) {
	data, err := os.ReadFile(p.Listed())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no export has listed review requests yet: run `plan-dash export` first")
	}
	if err != nil {
		return nil, err
	}
	var in struct {
		Requests []Request `json:"requests"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Listed(), err)
	}
	out := map[string]string{}
	for _, r := range in.Requests {
		if k := fmt.Sprintf("%s#%d", r.Repo, r.Number); key.MatchString(k) && fullHead.MatchString(r.Head) {
			out[k] = r.Head
		}
	}
	return out, nil
}

// docID is a dispatch document's ID, OWNER~NAME~N: a key with the
// characters a document ID may not hold replaced.
var docID = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]{0,99})~([A-Za-z0-9_][A-Za-z0-9_.-]{0,99})~([1-9][0-9]{0,9})$`)

// DocID is key's dispatch document's ID.
func DocID(k string) string {
	slug, n := Split(k)
	return strings.Replace(slug, "/", "~", 1) + "~" + strconv.Itoa(n)
}

// docFile is document id's file as an ArtifactData read with out_dir names
// it: "~" spelled "@".
func docFile(id string) string { return strings.ReplaceAll(id, "~", "@") + ".json" }

// unspell is the document ID a file's name without ".json" spells: "@" as
// the two separators of OWNER@NAME@N, as a read writes them, or name as it
// is. Neither part of a key holds "@" or "~", so no other ID spells the
// same.
func unspell(name string) string {
	if strings.Count(name, "@") == 2 && !strings.Contains(name, "~") {
		return strings.ReplaceAll(name, "@", "~")
	}
	return name
}

// maxDoc is the most of a page's dispatch document read; the page writes
// a few short fields.
const maxDoc = 16 << 10

// Doc is the dispatch/<ID> document Dispatch writes back for the page: an
// entry's state, or "refused" with why. Times are RFC 3339, or "". It has
// no "anyway": that is one click's, never a lasting flag.
type Doc struct {
	Schema int    `json:"schema"`
	State  string `json:"state"`
	Triage bool   `json:"triage"`
	At     string `json:"at"`
	// Head is the head asked for, the only one reviewed; Stale is whether
	// the page now lists another.
	Head   string `json:"head"`
	Stale  bool   `json:"stale"`
	Until  string `json:"until"`
	Reason string `json:"reason"`
}

// pageDoc is what Dispatch reads of a page's document: the page asks with
// state "requested", Triage on unless it says otherwise, "run anyway" as
// Anyway, and At, when it was asked, in RFC 3339. Every other field is
// ignored.
type pageDoc struct {
	State  string `json:"state"`
	Triage *bool  `json:"triage"`
	Anyway bool   `json:"anyway"`
	At     string `json:"at"`
}

// Result is what one Dispatch did, by key and document ID alone.
type Result struct {
	Queued  []string `json:"queued"`
	Anyway  []string `json:"anyway"`
	Refused []string `json:"refused"`
	// Ignored counts the files in the dispatch directory that named no
	// document Dispatch reads, which it removed.
	Ignored int `json:"ignored"`
	// Written are the IDs of the documents written back, for the session
	// to put on the page.
	Written []string `json:"written"`
	Started bool     `json:"started"`
	Off     bool     `json:"off"`
}

// opened runs once Dispatch has opened the dispatch directory; tests swap
// the directory there.
var opened = func() {}

// Refusals, as the page shows them.
const (
	notListed  = "not a review request the page lists"
	unreadable = "not a dispatch document: too large, not JSON, or not a file"
	ambiguous  = "two files name this document"
)

// Dispatch takes the page's requests from Out/dispatch into the queue: only
// a key the last export listed, named by its document's ID, with the head
// the export listed. It writes each entry's state back there, a refused
// request's as "refused", removes what names no document, and calls start
// when something waits to run and the kill switch is off. Nothing a
// document holds but its three fields is read, and no text from one is
// written anywhere.
func Dispatch(p Paths, now time.Time, start func() error) (Result, error) {
	res := Result{Queued: []string{}, Anyway: []string{}, Refused: []string{}, Written: []string{}}
	heads, err := listed(p)
	if err != nil {
		return res, err
	}
	dir, err := OpenOut(p, "dispatch")
	if err != nil {
		return res, err
	}
	defer dir.Close()
	opened()
	files, err := ReadNames(dir)
	if err != nil {
		return res, err
	}
	type request struct {
		key            string
		triage, anyway bool
		at             time.Time
	}
	var requests []request
	refused := map[string]string{}
	named := map[string][]string{}
	var ids []string
	for _, f := range files {
		id, ok := strings.CutSuffix(f.Name(), ".json")
		id = unspell(id)
		switch {
		case f.IsDir():
			res.Ignored++
			continue
		case !ok || !docID.MatchString(id):
			dir.Remove(f.Name())
			res.Ignored++
			continue
		}
		if named[id] == nil {
			ids = append(ids, id)
		}
		named[id] = append(named[id], f.Name())
	}
	for _, id := range ids {
		m := docID.FindStringSubmatch(id)
		k := m[1] + "/" + m[2] + "#" + m[3]
		doc, ok := pageDoc{}, false
		if len(named[id]) == 1 {
			doc, ok = readDoc(dir, named[id][0])
		}
		// What is written back is spelled as a read spells it, so the next
		// read replaces it; another spelling is removed once read.
		for _, name := range named[id] {
			if name != docFile(id) {
				dir.Remove(name)
			}
		}
		switch {
		case len(named[id]) > 1:
			refused[id] = ambiguous
		case !ok:
			refused[id] = unreadable
		case doc.State != "requested":
		case heads[k] == "":
			refused[id] = notListed
		default:
			at, _ := time.Parse(time.RFC3339, doc.At)
			requests = append(requests, request{k, doc.Triage == nil || *doc.Triage, doc.Anyway, at})
		}
	}

	entries, err := update(p, func(entries []Entry) ([]Entry, bool, error) {
		for _, r := range requests {
			i := find(entries, r.key)
			switch {
			case i < 0:
				entries = append(entries, Entry{Key: r.key, State: Queued, Head: heads[r.key], Triage: r.triage,
					Queued: now, Changed: now, Review: NoExit, Triaged: NoExit})
				res.Queued = append(res.Queued, r.key)
			// The page keeps a request's document until the session writes
			// the entry's state over it, so one asked for no later than the
			// entry was last queued is that request again.
			case Final(entries[i].State) && r.at.After(entries[i].Queued):
				entries[i] = Entry{Key: r.key, State: Queued, Head: heads[r.key], Triage: r.triage,
					Queued: now, Changed: now, Review: NoExit, Triaged: NoExit}
				res.Queued = append(res.Queued, r.key)
			// "Run anyway" is for an entry the quota gate postponed, and
			// passes the gate on its next run only.
			case entries[i].State == Postponed && r.anyway && r.at.After(entries[i].Changed):
				entries[i].State, entries[i].Anyway, entries[i].Until, entries[i].Changed = Queued, true, time.Time{}, now
				res.Anyway = append(res.Anyway, r.key)
			}
		}
		// A finished review of a request no longer listed has nothing left
		// to show.
		kept := entries[:0]
		for _, e := range entries {
			if Final(e.State) && heads[e.Key] == "" {
				dir.Remove(docFile(DocID(e.Key)))
				continue
			}
			kept = append(kept, e)
		}
		return kept, true, nil
	})
	if err != nil {
		return res, err
	}

	var errs []error
	written := map[string]bool{}
	for id, why := range refused {
		res.Refused = append(res.Refused, id)
		errs = append(errs, WriteObject(dir, docFile(id), Doc{Schema: agentplans.Schema, State: "refused", At: stamp(now),
			Reason: why}))
		written[id] = true
	}
	waiting := false
	for _, e := range entries {
		waiting = waiting || e.State == Queued || e.State == Running || e.State == Postponed && !now.Before(e.Until)
		errs = append(errs, WriteObject(dir, docFile(DocID(e.Key)), docOf(e, heads)))
		written[DocID(e.Key)] = true
	}
	res.Written = slices.Sorted(maps.Keys(written))
	slices.Sort(res.Refused)
	res.Off = p.IsOff()
	if waiting && !res.Off {
		if err := start(); err != nil {
			errs = append(errs, err)
		} else {
			res.Started = true
		}
	}
	return res, errors.Join(errs...)
}

func find(entries []Entry, k string) int {
	for i, e := range entries {
		if e.Key == k {
			return i
		}
	}
	return -1
}

// readDoc reads a page's document from a regular file in dir of at most
// maxDoc.
func readDoc(dir *os.Root, name string) (pageDoc, bool) {
	var doc pageDoc
	if !ReadObject(dir, name, maxDoc, &doc) {
		return pageDoc{}, false
	}
	return doc, true
}

// docOf is e's document, with Stale against heads, the page's listing.
func docOf(e Entry, heads map[string]string) Doc {
	d := Doc{Schema: agentplans.Schema, State: e.State, Triage: e.Triage, At: stamp(e.Changed), Head: e.Head,
		Until: stamp(e.Until), Reason: reason(e)}
	if h := heads[e.Key]; h != "" && e.Head != "" && h != e.Head {
		d.Stale = true
	}
	return d
}

// reason is why e is in its state, for the page, built from its key and
// exit statuses alone.
func reason(e Entry) string {
	switch {
	case e.State == Postponed:
		return "a quota window is at or past 90%"
	case e.State == Stale:
		return "the pull request is no longer listed, or no longer at the head asked for; ask again from the page"
	case (e.State == Queued || e.State == Running) && e.Restarted:
		return "run again: its last run was stopped"
	case e.State == NeedsOperator && e.Review == 5:
		return fmt.Sprintf("the vet raised red flags, so the tests did not run; the operator decides whether "+
			"`pr-loop review --run %s` runs them", e.Key)
	case e.State == NeedsOperator && e.Review == 4:
		return "Codex cannot review here and triage is off; review it with a sub-agent"
	case e.State == Failed && e.Triaged != NoExit:
		return fmt.Sprintf("triage exited %d", e.Triaged)
	case e.State == Failed && e.Review != NoExit:
		return fmt.Sprintf("pr-loop review exited %d", e.Review)
	case e.State == Failed:
		return "the review did not start"
	}
	return ""
}

// Unit is the declared unit that runs the queue.
const Unit = "agent-plans-queue.service"

// StartUnit asks systemd to start Unit without waiting for it: started
// already, it goes on to what was added, rereading the queue after each
// entry.
func StartUnit() error {
	cmd := exec.Command("systemctl", "--user", "start", "--no-block", Unit)
	cmd.Env = Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user start %s: %w: %s", Unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Environ is the environment without AGENT_PLANS_SESSION, which marks the
// dashboard session alone, for every command run from here.
func Environ() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AGENT_PLANS_SESSION=") {
			env = append(env, kv)
		}
	}
	return env
}
