package loop

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/relay"
)

type fakeCheck struct {
	name, status, conclusion string
	job                      int64
}

type fakeCIPR struct {
	author, state, head string
	cross               bool
	headOid             string // the PR's head
	branchOid           string // the head branch's tip on GitHub
}

// fakeCI answers the GraphQL that ci, wait ci and logs make, for the viewer
// "me" in o/r. tick, when set, runs on every sleep of a wait.
type fakeCI struct {
	prs    map[int]*fakeCIPR
	checks map[string][]fakeCheck // by commit
	base   []fakeCheck
	tick   func()
}

func contexts(checks []fakeCheck) any {
	nodes := []any{}
	for _, c := range checks {
		nodes = append(nodes, map[string]any{"__typename": "CheckRun", "name": c.name, "status": c.status,
			"conclusion": c.conclusion, "detailsUrl": "https://ci/" + c.name, "databaseId": c.job, "isRequired": false})
	}
	return map[string]any{"statusCheckRollup": map[string]any{"contexts": map[string]any{"nodes": nodes}}}
}

func (f *fakeCI) graphQL(query string, vars map[string]any, out any) error {
	head := func(n int, pr *fakeCIPR) map[string]any {
		return map[string]any{"number": n, "state": pr.state, "isCrossRepository": pr.cross, "author": map[string]any{"login": pr.author}}
	}
	var data any
	switch {
	case strings.Contains(query, "viewer { login }"):
		data = map[string]any{"viewer": map[string]any{"login": "me"},
			"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}}
	case strings.Contains(query, "object(oid: $oid)"):
		data = map[string]any{"repository": map[string]any{"object": contexts(f.checks[vars["oid"].(string)])}}
	case strings.Contains(query, "headRefOid headRef"):
		pr := f.prs[vars["number"].(int)]
		if pr == nil {
			return errors.New("GraphQL: Could not resolve to a PullRequest")
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefName": pr.head, "headRefOid": pr.headOid, "headRef": map[string]any{"target": map[string]any{"oid": pr.branchOid}},
			"baseRef": map[string]any{"target": contexts(f.base)}}}}
	case strings.Contains(query, "pullRequests(headRefName"):
		nodes := []any{}
		for n, pr := range f.prs {
			if pr.head == vars["head"] && pr.state == "OPEN" {
				nodes = append(nodes, head(n, pr))
			}
		}
		data = map[string]any{"repository": map[string]any{"pullRequests": map[string]any{"nodes": nodes}}}
	case strings.Contains(query, "pullRequest(number: $number)"):
		pr := f.prs[vars["number"].(int)]
		if pr == nil {
			return errors.New("GraphQL: Could not resolve to a PullRequest")
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": head(vars["number"].(int), pr)}}
	default:
		return errors.New("unexpected query: " + query)
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

// ciRepo makes a checkout of o/r on branch feat, whose PR is 1, with a clock
// that sleeps without waiting.
func ciRepo(t *testing.T) *fakeCI {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(relay.RelayEnv, "")
	t.Setenv(sessionEnv, "")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, dir, "switch", "-qc", "feat")
	git.Dir = dir
	t.Cleanup(func() { git.Dir = "" })

	f := &fakeCI{checks: map[string][]fakeCheck{}, prs: map[int]*fakeCIPR{
		1: {author: "me", state: "OPEN", head: "feat", headOid: "a1", branchOid: "a1"},
		2: {author: "josh", state: "OPEN", head: "theirs", headOid: "b1", branchOid: "b1"},
		3: {author: "me", state: "MERGED", head: "done", headOid: "c1", branchOid: "c1"},
		4: {author: "me", state: "OPEN", head: "forked", cross: true, headOid: "d1", branchOid: "d1"},
	}}
	oldGraphQL, oldNow, oldSleep := graphQL, now, sleep
	clock := time.Unix(0, 0)
	graphQL = f.graphQL
	now = func() time.Time { return clock }
	sleep = func(d time.Duration) {
		clock = clock.Add(d)
		if f.tick != nil {
			f.tick()
		}
	}
	t.Cleanup(func() { graphQL, now, sleep = oldGraphQL, oldNow, oldSleep })
	return f
}

func targets(t *testing.T, args ...string) ([]int, error) {
	t.Helper()
	_, numbers, err := ciTargets(args)
	return numbers, err
}

func TestCITargetsOutsideALoop(t *testing.T) {
	ciRepo(t)
	for _, args := range [][]string{nil, {"1"}, {"#1"}, {"feat"}} {
		if got, err := targets(t, args...); err != nil || !reflect.DeepEqual(got, []int{1}) {
			t.Errorf("%v: %v, %v", args, got, err)
		}
	}
	// A local branch is found through its upstream on GitHub.
	gitIn(t, git.Dir, "branch", "mine")
	gitIn(t, git.Dir, "config", "branch.mine.remote", "origin")
	gitIn(t, git.Dir, "config", "branch.mine.merge", "refs/heads/feat")
	if got, err := targets(t, "mine"); err != nil || !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("branch with an upstream: %v, %v", got, err)
	}
	for _, args := range [][]string{{"2"}, {"3"}, {"theirs"}, {"forked"}, {"done"}, {"1", "2"}} {
		if got, err := targets(t, args...); !refused(err) {
			t.Errorf("%v was accepted: %v, %v", args, got, err)
		}
	}
	if _, err := targets(t, "9"); err == nil {
		t.Error("a PR that does not exist was accepted")
	}

	gitIn(t, git.Dir, "switch", "-qc", "unpublished")
	if _, err := targets(t); !refused(err) {
		t.Errorf("a branch without a PR: %v", err)
	}
	gitIn(t, git.Dir, "switch", "-q", "--detach")
	if _, err := targets(t); !refused(err) {
		t.Errorf("a detached HEAD: %v", err)
	}
	// Where HEAD heads no PR, a registered loop's PRs are the default.
	registerLoop(t)
	if got, err := targets(t); err != nil || !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("detached beside a loop: %v, %v", got, err)
	}
}

// A loop session reads what it named, or the loop's PRs, as it always has.
func TestCITargetsInALoop(t *testing.T) {
	for _, env := range []string{relay.RelayEnv, sessionEnv} {
		t.Run(env, func(t *testing.T) {
			ciRepo(t)
			t.Setenv(env, "1")
			if _, err := targets(t); !refused(err) || !strings.Contains(err.Error(), "begin") {
				t.Errorf("a session without a loop: %v", err)
			}
			registerLoop(t)
			for args, want := range map[string][]int{"": {1}, "feat": {1}, "2": {2}} {
				got, err := targets(t, strings.Fields(args)...)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("%q: %v, %v", args, got, err)
				}
			}
			if _, err := targets(t, "theirs"); !refused(err) {
				t.Errorf("a branch outside the loop: %v", err)
			}
		})
	}
}

var passing = []fakeCheck{{name: "build", status: "COMPLETED", conclusion: "SUCCESS", job: 1}}

// Right after a push the PR can still show the previous head, whose checks
// have finished; they are not the new head's result.
func TestWaitCIKeysOnTheNewHead(t *testing.T) {
	f := ciRepo(t)
	f.checks["a1"] = passing
	f.prs[1].branchOid = "a2"
	ci, err := ciStatus("o/r", 1, "")
	if err != nil || ci.State != "awaitingHead" || ci.ExpectedOid != "a2" || len(ci.Failed)+len(ci.Pending) != 0 {
		t.Errorf("a lagging head: %+v, %v", ci, err)
	}

	polls := 0
	f.tick = func() {
		polls++
		switch polls {
		case 2:
			f.prs[1].headOid = "a2"
		case 3:
			f.checks["a2"] = []fakeCheck{{name: "build", status: "IN_PROGRESS"}}
		case 4:
			f.checks["a2"] = []fakeCheck{{name: "build", status: "COMPLETED", conclusion: "FAILURE", job: 7}}
		}
	}
	result, settled, err := WaitCI(nil, "", time.Hour, time.Minute, 3*time.Minute)
	if err != nil || !settled || result.Result != "failed" || polls != 4 {
		t.Fatalf("wait: %+v, settled %v, %v after %d polls", result, settled, err, polls)
	}
	if c := result.PRs[0]; c.HeadOid != "a2" || c.Failed[0].Logs != "pr-loop logs 1 build" {
		t.Errorf("the new head's result: %+v", c)
	}
}

// --sha waits for a commit GitHub does not show yet, and gives up at the
// timeout without reporting the old head's checks.
func TestWaitCITimesOut(t *testing.T) {
	f := ciRepo(t)
	f.checks["a1"] = passing
	sha := strings.Repeat("f", 40)
	start := now()
	result, settled, err := WaitCI([]string{"feat"}, sha, 5*time.Minute, 2*time.Minute, 0)
	if err != nil || settled || result.Result != "timedOut" {
		t.Fatalf("wait: %+v, settled %v, %v", result, settled, err)
	}
	if c := result.PRs[0]; c.State != "awaitingHead" || c.ExpectedOid != sha || c.HeadOid != "a1" {
		t.Errorf("timed out on %+v", c)
	}
	if waited := now().Sub(start); waited != 5*time.Minute {
		t.Errorf("waited %s", waited)
	}
	if _, _, err := WaitCI([]string{"1", "feat"}, sha, 0, 0, 0); !refused(err) {
		t.Errorf("--sha with two PRs: %v", err)
	}

	// Within its grace, a head without checks is still waiting for them.
	f.checks = map[string][]fakeCheck{}
	result, settled, _ = WaitCI(nil, "", time.Minute, time.Minute, 3*time.Minute)
	if settled || result.Result != "timedOut" || result.PRs[0].State != "none" {
		t.Errorf("no checks within the grace: %+v %+v", result, result.PRs[0])
	}
}

// A head that still has no checks a grace period after it became the
// expected head is a result of its own, counted from when it was seen.
func TestWaitCINoChecks(t *testing.T) {
	f := ciRepo(t)
	start := now()
	result, settled, err := WaitCI(nil, "", time.Hour, time.Minute, 3*time.Minute)
	if err != nil || !settled || result.Result != "noChecks" || result.PRs[0].State != "none" {
		t.Fatalf("no checks: %+v, settled %v, %v", result, settled, err)
	}
	if waited := now().Sub(start); waited != 3*time.Minute {
		t.Errorf("waited %s", waited)
	}

	// The old head's checks are not the new head's, and the grace starts only
	// once the PR shows the new head.
	f.checks["a1"] = passing
	f.prs[1].branchOid = "a2"
	polls := 0
	f.tick = func() {
		if polls++; polls == 2 {
			f.prs[1].headOid = "a2"
		}
	}
	start = now()
	result, settled, _ = WaitCI(nil, "", time.Hour, time.Minute, 3*time.Minute)
	if !settled || result.Result != "noChecks" || result.PRs[0].HeadOid != "a2" {
		t.Errorf("no checks on the new head: %+v", result)
	}
	if waited := now().Sub(start); waited != 5*time.Minute {
		t.Errorf("waited %s for a head seen after 2m", waited)
	}

	// A push during the grace restarts it for the new head.
	polls = 0
	f.tick = func() {
		if polls++; polls == 2 {
			f.prs[1].headOid, f.prs[1].branchOid = "a3", "a3"
		}
	}
	start = now()
	if result, _, _ = WaitCI(nil, "", time.Hour, time.Minute, 3*time.Minute); result.Result != "noChecks" {
		t.Errorf("after a second push: %+v", result)
	}
	if waited := now().Sub(start); waited != 5*time.Minute {
		t.Errorf("waited %s for a head pushed after 2m", waited)
	}

	// Checks that register within the grace are waited for.
	polls = 0
	f.tick = func() {
		if polls++; polls == 2 {
			f.checks["a3"] = passing
		}
	}
	if result, _, _ = WaitCI(nil, "", time.Hour, time.Minute, 3*time.Minute); result.Result != "passed" {
		t.Errorf("checks within the grace: %+v", result)
	}

	// A failure elsewhere outranks a head without checks.
	f.tick = nil
	f.prs[1].headOid, f.prs[1].branchOid = "a4", "a4"
	f.prs[5] = &fakeCIPR{author: "me", state: "OPEN", head: "other", headOid: "e1", branchOid: "e1"}
	f.checks["e1"] = []fakeCheck{{name: "build", status: "COMPLETED", conclusion: "FAILURE", job: 1}}
	if result, settled, _ = WaitCI([]string{"1", "5"}, "", time.Hour, time.Minute, 0); !settled || result.Result != "failed" {
		t.Errorf("a failure beside no checks: %+v", result)
	}
}

// A failure is the PR's own unless the same check fails on the base.
func TestWaitCIComparesWithTheBase(t *testing.T) {
	f := ciRepo(t)
	failing := func(names ...string) []fakeCheck {
		var out []fakeCheck
		for i, n := range names {
			out = append(out, fakeCheck{name: n, status: "COMPLETED", conclusion: "FAILURE", job: int64(i + 1)})
		}
		return out
	}
	f.base = append(failing("lint"), fakeCheck{name: "test (unit)", status: "COMPLETED", conclusion: "SUCCESS"})
	f.checks["a1"] = append(failing("lint"), passing...)
	result, settled, err := WaitCI(nil, "", 0, 0, 0)
	if err != nil || !settled || result.Result != "failedOnBase" || result.PRs[0].Failed[0].OnBase != "failed" {
		t.Errorf("a failure the base shares: %+v, %v", result, err)
	}
	f.checks["a1"] = failing("lint", "test (unit)", "new")
	result, _, _ = WaitCI(nil, "", 0, 0, 0)
	onBase := map[string]string{}
	for _, c := range result.PRs[0].Failed {
		onBase[c.Name] = c.OnBase
	}
	if result.Result != "failed" || !reflect.DeepEqual(onBase, map[string]string{"lint": "failed", "test (unit)": "passed", "new": "absent"}) {
		t.Errorf("failures of the PR's own: %s %v", result.Result, onBase)
	}
	if logs := result.PRs[0].Failed[1].Logs; logs != "pr-loop logs 1 'test (unit)'" {
		t.Errorf("logs command: %s", logs)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"build": "build", "a b": "'a b'", `it's "$x"`: `"it's \"\$x\""`} {
		if got := shellQuote(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
