package loop

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
)

// Check is one CI check on a commit.
type Check struct {
	Name     string `json:"name"`
	State    string `json:"state"` // pending, passed or failed
	Required bool   `json:"required"`
	URL      string `json:"url"`
	Job      int64  `json:"job,omitempty"`
	// Logs is the command that prints a failed GitHub Actions job's log.
	Logs string `json:"logs,omitempty"`
	// OnBase is the same check's state on the base branch, or "absent" when
	// the base never ran it, which proves nothing either way.
	OnBase string `json:"onBase,omitempty"`
}

// CI summarises the checks on a PR's head.
type CI struct {
	PR      int    `json:"pr"`
	Branch  string `json:"branch"`
	HeadOid string `json:"headOid"`
	// ExpectedOid is the commit the PR's head should be: the one asked for,
	// or the head branch's tip on GitHub when the PR has not caught up with
	// a push. Until headOid matches it, State is awaitingHead and no checks
	// are reported, so a previous head's checks are never taken for its own.
	ExpectedOid string   `json:"expectedOid,omitempty"`
	State       string   `json:"state"` // awaitingHead, none, pending, failed or passed
	Pending     []string `json:"pending"`
	Failed      []Check  `json:"failed"`
}

// finished reports whether CI has reached a result on the expected head.
func (c *CI) finished() bool {
	return c.State == "passed" || c.State == "failed"
}

const contextFields = `__typename
	... on CheckRun { name status conclusion detailsUrl databaseId isRequired(pullRequestNumber: $number) }
	... on StatusContext { context state targetUrl isRequired(pullRequestNumber: $number) }`

type rollup struct {
	Contexts struct {
		Nodes []struct {
			Typename   string `json:"__typename"`
			Name       string
			Status     string
			Conclusion string
			DetailsURL string
			DatabaseID int64
			Context    string
			State      string
			TargetURL  string
			IsRequired bool
		}
	}
}

var failedConclusions = map[string]bool{"FAILURE": true, "TIMED_OUT": true, "CANCELLED": true, "ACTION_REQUIRED": true, "STARTUP_FAILURE": true}

func (r *rollup) checks() []Check {
	if r == nil {
		return nil
	}
	var out []Check
	for _, c := range r.Contexts.Nodes {
		if c.Typename == "CheckRun" {
			state := "passed"
			if c.Status != "COMPLETED" {
				state = "pending"
			} else if failedConclusions[c.Conclusion] {
				state = "failed"
			}
			out = append(out, Check{Name: c.Name, State: state, Required: c.IsRequired, URL: c.DetailsURL, Job: c.DatabaseID})
			continue
		}
		state := map[string]string{"PENDING": "pending", "EXPECTED": "pending", "SUCCESS": "passed"}[c.State]
		if state == "" {
			state = "failed"
		}
		out = append(out, Check{Name: c.Context, State: state, Required: c.IsRequired, URL: c.TargetURL})
	}
	return out
}

// ciStatus reports the checks on a PR's head commit, compared with its base.
// The head is expected to be want, or by default the head branch's tip on
// GitHub, which a push moves at once while the PR's head follows later.
func ciStatus(slug string, number int, want string) (*CI, error) {
	owner, name, _ := strings.Cut(slug, "/")
	vars := map[string]any{"owner": owner, "name": name, "number": number}
	var data struct {
		Repository struct {
			PullRequest struct {
				HeadRefName string
				HeadRefOid  string
				HeadRef     *struct {
					Target struct{ Oid string }
				}
				BaseRef *struct {
					Target struct{ StatusCheckRollup *rollup }
				}
			}
		}
	}
	err := graphQL(fmt.Sprintf(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			headRefName headRefOid headRef { target { oid } }
			baseRef { target { ... on Commit { statusCheckRollup { contexts(first: 100) { nodes { %s } } } } } }
		} }
	}`, contextFields), vars, &data)
	if err != nil {
		return nil, err
	}
	pr := data.Repository.PullRequest
	ci := &CI{PR: number, Branch: pr.HeadRefName, HeadOid: pr.HeadRefOid, ExpectedOid: want, Pending: []string{}, Failed: []Check{}}
	if want == "" && pr.HeadRef != nil && pr.HeadRef.Target.Oid != pr.HeadRefOid {
		ci.ExpectedOid = pr.HeadRef.Target.Oid
	}
	if ci.ExpectedOid != "" && ci.ExpectedOid != pr.HeadRefOid {
		ci.State = "awaitingHead"
		return ci, nil
	}
	var head struct {
		Repository struct {
			Object *struct{ StatusCheckRollup *rollup }
		}
	}
	vars["oid"] = pr.HeadRefOid
	err = graphQL(fmt.Sprintf(`query($owner: String!, $name: String!, $number: Int!, $oid: GitObjectID!) {
		repository(owner: $owner, name: $name) {
			object(oid: $oid) { ... on Commit { statusCheckRollup { contexts(first: 100) { nodes { %s } } } } }
		}
	}`, contextFields), vars, &head)
	if err != nil {
		return nil, err
	}
	var checks []Check
	if head.Repository.Object != nil {
		checks = head.Repository.Object.StatusCheckRollup.checks()
	}
	onBase := map[string]string{}
	if pr.BaseRef != nil {
		for _, c := range pr.BaseRef.Target.StatusCheckRollup.checks() {
			onBase[c.Name] = c.State
		}
	}
	for _, c := range checks {
		switch c.State {
		case "pending":
			ci.Pending = append(ci.Pending, c.Name)
		case "failed":
			c.OnBase = onBase[c.Name]
			if c.OnBase == "" {
				c.OnBase = "absent"
			}
			if c.Job != 0 {
				c.Logs = fmt.Sprintf("pr-loop logs %d %s", number, shellQuote(c.Name))
			}
			ci.Failed = append(ci.Failed, c)
		}
	}
	switch {
	case len(checks) == 0:
		ci.State = "none"
	case len(ci.Pending) > 0:
		ci.State = "pending"
	case len(ci.Failed) > 0:
		ci.State = "failed"
	default:
		ci.State = "passed"
	}
	return ci, nil
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9._/:=+-]+$`)

// shellQuote quotes s for bash and fish alike.
func shellQuote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s) + `"`
}

// CIStatuses reports CI on the heads of the PRs ciTargets resolves.
func CIStatuses(args []string) (any, error) {
	slug, numbers, err := ciTargets(args)
	if err != nil {
		return nil, err
	}
	out := []*CI{}
	for _, n := range numbers {
		ci, err := ciStatus(slug, n, "")
		if err != nil {
			return nil, err
		}
		out = append(out, ci)
	}
	return out, nil
}

// CIWait is what WaitCI found.
type CIWait struct {
	// Result is passed; failed, when a check fails that does not fail on the
	// base; failedOnBase, when every failing check fails on the base too; or
	// timedOut, when some PR's state is still awaitingHead, none or pending.
	Result string `json:"result"`
	PRs    []*CI  `json:"prs"`
}

var (
	// now and sleep are replaced by tests.
	now   = time.Now
	sleep = time.Sleep
)

// WaitCI polls until CI has finished on every PR's expected head, or the
// timeout passes. sha names the commit a single PR's head is expected to be.
func WaitCI(args []string, sha string, timeout, interval time.Duration) (*CIWait, bool, error) {
	slug, numbers, err := ciTargets(args)
	if err != nil {
		return nil, false, err
	}
	want := ""
	if sha != "" {
		if len(numbers) != 1 {
			return nil, false, refuse("--sha takes exactly one PR")
		}
		if want, err = fullOid(sha); err != nil {
			return nil, false, err
		}
	}
	deadline := now().Add(timeout)
	for {
		out := &CIWait{Result: "passed", PRs: []*CI{}}
		settled := true
		for _, n := range numbers {
			ci, err := ciStatus(slug, n, want)
			if err != nil {
				return nil, false, err
			}
			out.PRs = append(out.PRs, ci)
			settled = settled && ci.finished()
			for _, c := range ci.Failed {
				if c.OnBase != "failed" {
					out.Result = "failed"
				} else if out.Result == "passed" {
					out.Result = "failedOnBase"
				}
			}
		}
		left := deadline.Sub(now())
		if settled || left <= 0 {
			if !settled {
				out.Result = "timedOut"
			}
			return out, settled, nil
		}
		sleep(min(interval, left))
	}
}

var hexOid = regexp.MustCompile(`^[0-9a-f]{40}$`)

// fullOid resolves a revision to a full commit ID, accepting a full ID this
// checkout lacks.
func fullOid(rev string) (string, error) {
	if hexOid.MatchString(rev) {
		return rev, nil
	}
	if oid, err := git.Run("rev-parse", "--verify", "-q", rev+"^{commit}"); err == nil {
		return oid, nil
	}
	return "", refuse("%s is not a commit here; pass the full commit ID", rev)
}

var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[@-_]`)

// Logs returns the end of a failed GitHub Actions job's log on a PR's head.
func Logs(pr string, check string, lines int) (string, error) {
	slug, numbers, err := ciTargets([]string{pr})
	if err != nil {
		return "", err
	}
	number := numbers[0]
	ci, err := ciStatus(slug, number, "")
	if err != nil {
		return "", err
	}
	for _, c := range ci.Failed {
		if c.Name != check {
			continue
		}
		if c.Job == 0 {
			return "", refuse("%s is not a GitHub Actions job; its log is at %s", check, c.URL)
		}
		raw, err := gh.Raw(fmt.Sprintf("repos/%s/actions/jobs/%d/logs", slug, c.Job))
		if err != nil {
			return "", err
		}
		all := strings.Split(escapes.ReplaceAllString(string(raw), ""), "\n")
		if len(all) > lines {
			all = all[len(all)-lines:]
		}
		return strings.Join(all, "\n"), nil
	}
	if ci.State == "awaitingHead" {
		return "", refuse("#%d's head is not yet %s on GitHub; wait for CI first", number, ci.ExpectedOid)
	}
	return "", refuse("no failed check named %q on #%d", check, number)
}

// loopNumbers resolves which PRs a read-only command looks at: the ones given
// (in any repository checkout), or the loop's own.
func loopNumbers(numbers []int) (string, []int, *State, error) {
	state, err := loadState(len(numbers) == 0)
	if err != nil {
		return "", nil, nil, err
	}
	slug, err := slugOf(state)
	if err != nil {
		return "", nil, nil, err
	}
	if len(numbers) == 0 {
		for _, b := range state.Order {
			numbers = append(numbers, state.Branches[b].PR)
		}
	}
	return slug, numbers, state, nil
}

// ciTargets resolves the PRs that ci, wait ci and logs read, each named by
// number or by branch. In a loop session (see inLoopSession) they are those
// named, by number or registered branch, or by default the loop's. Elsewhere
// each must be an open PR by the authenticated user in this checkout's
// repository, by default the current branch's; where HEAD heads no such PR
// and a loop is registered here, the default is the loop's PRs.
func ciTargets(args []string) (string, []int, error) {
	if inLoopSession() {
		return loopTargets(args)
	}
	repo, err := repository()
	if err != nil {
		return "", nil, err
	}
	if len(args) == 0 {
		branch, err := currentBranch()
		if err == nil {
			var n int
			if n, err = branchPR(repo, branch); err == nil {
				return repo.Slug, []int{n}, nil
			}
		}
		var r Refusal
		if !errors.As(err, &r) {
			return "", nil, err
		}
		active, aerr := Active()
		switch {
		case aerr != nil:
			return "", nil, aerr
		case active:
			return loopTargets(nil)
		}
		return "", nil, err
	}
	var out []int
	for _, a := range args {
		n, ok := prNumber(a)
		if ok {
			err = checkPR(repo, n)
		} else {
			n, err = branchPR(repo, a)
		}
		if err != nil {
			return "", nil, err
		}
		out = append(out, n)
	}
	return repo.Slug, out, nil
}

func prNumber(arg string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
	return n, err == nil && n > 0
}

// loopTargets is what a loop session reads: the PRs named, by number or by a
// registered branch, or the loop's.
func loopTargets(args []string) (string, []int, error) {
	var numbers []int
	for _, a := range args {
		n, ok := prNumber(a)
		if !ok {
			state, err := loadState(false)
			if err != nil {
				return "", nil, err
			}
			if state == nil || state.Branches[a] == nil {
				return "", nil, refuse("%q is neither a PR number nor a branch of this loop", a)
			}
			n = state.Branches[a].PR
		}
		numbers = append(numbers, n)
	}
	slug, numbers, _, err := loopNumbers(numbers)
	return slug, numbers, err
}

type prHead struct {
	Number            int
	State             string
	IsCrossRepository bool
	Author            *login
}

func (p *prHead) byViewer(repo *Repo) bool {
	return p != nil && p.State == "OPEN" && p.Author != nil && p.Author.Login == repo.Viewer
}

const prHeadFields = `number state isCrossRepository author { login }`

// checkPR refuses a number that is not an open PR by the viewer in repo.
func checkPR(repo *Repo, number int) error {
	owner, name, _ := strings.Cut(repo.Slug, "/")
	var data struct {
		Repository struct{ PullRequest *prHead }
	}
	err := graphQL(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) { `+prHeadFields+` } }
	}`, map[string]any{"owner": owner, "name": name, "number": number}, &data)
	if err == nil && !data.Repository.PullRequest.byViewer(repo) {
		err = refuse("#%d is not an open pull request by %s in %s", number, repo.Viewer, repo.Slug)
	}
	return err
}

// branchPR finds the open PR by the viewer whose head is local's upstream
// branch, when that is on repo, or else the branch named local on GitHub.
func branchPR(repo *Repo, local string) (int, error) {
	remotes, err := githubRemotes()
	if err != nil {
		return 0, err
	}
	head := local
	remote, _ := git.Run("config", "--get", "branch."+local+".remote")
	merge, _ := git.Run("config", "--get", "branch."+local+".merge")
	if remotes[remote] == repo.Slug && strings.HasPrefix(merge, "refs/heads/") {
		head = strings.TrimPrefix(merge, "refs/heads/")
	}
	owner, name, _ := strings.Cut(repo.Slug, "/")
	var data struct {
		Repository struct {
			PullRequests struct{ Nodes []*prHead }
		}
	}
	err = graphQL(`query($owner: String!, $name: String!, $head: String!) {
		repository(owner: $owner, name: $name) {
			pullRequests(headRefName: $head, states: [OPEN], first: 20) { nodes { `+prHeadFields+` } }
		}
	}`, map[string]any{"owner": owner, "name": name, "head": head}, &data)
	if err != nil {
		return 0, err
	}
	for _, pr := range data.Repository.PullRequests.Nodes {
		if pr.byViewer(repo) && !pr.IsCrossRepository {
			return pr.Number, nil
		}
	}
	return 0, refuse("%s is not the head of an open pull request by %s in %s", head, repo.Viewer, repo.Slug)
}
