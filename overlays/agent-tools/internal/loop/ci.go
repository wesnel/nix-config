package loop

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
)

// Check is one CI check on a commit.
type Check struct {
	Name     string `json:"name"`
	State    string `json:"state"` // pending, passed or failed
	Required bool   `json:"required"`
	URL      string `json:"url"`
	Job      int64  `json:"job,omitempty"`
	// OnBase is the same check's state on the base branch, or "absent" when
	// the base never ran it, which proves nothing either way.
	OnBase string `json:"onBase,omitempty"`
}

// CI summarises the checks on a PR's head.
type CI struct {
	PR      int      `json:"pr"`
	HeadOid string   `json:"headOid"`
	State   string   `json:"state"` // none, pending, failed or passed
	Pending []string `json:"pending"`
	Failed  []Check  `json:"failed"`
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

func ciStatus(slug string, number int) (*CI, error) {
	owner, name, _ := strings.Cut(slug, "/")
	var data struct {
		Repository struct {
			PullRequest struct {
				HeadRefOid string
				Commits    struct {
					Nodes []struct {
						Commit struct{ StatusCheckRollup *rollup }
					}
				}
				BaseRef *struct {
					Target struct{ StatusCheckRollup *rollup }
				}
			}
		}
	}
	err := gh.GraphQL(fmt.Sprintf(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			headRefOid
			commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes { %s } } } } } }
			baseRef { target { ... on Commit { statusCheckRollup { contexts(first: 100) { nodes { %s } } } } } }
		} }
	}`, contextFields, contextFields), map[string]any{"owner": owner, "name": name, "number": number}, &data)
	if err != nil {
		return nil, err
	}
	pr := data.Repository.PullRequest
	var head []Check
	if len(pr.Commits.Nodes) > 0 {
		head = pr.Commits.Nodes[0].Commit.StatusCheckRollup.checks()
	}
	onBase := map[string]string{}
	if pr.BaseRef != nil {
		for _, c := range pr.BaseRef.Target.StatusCheckRollup.checks() {
			onBase[c.Name] = c.State
		}
	}
	ci := &CI{PR: number, HeadOid: pr.HeadRefOid, Pending: []string{}, Failed: []Check{}}
	for _, c := range head {
		switch c.State {
		case "pending":
			ci.Pending = append(ci.Pending, c.Name)
		case "failed":
			c.OnBase = onBase[c.Name]
			if c.OnBase == "" {
				c.OnBase = "absent"
			}
			ci.Failed = append(ci.Failed, c)
		}
	}
	switch {
	case len(head) == 0:
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

// CIStatuses reports CI on each of the loop's heads (or the given PRs).
func CIStatuses(numbers []int) (any, error) {
	slug, numbers, _, err := loopNumbers(numbers)
	if err != nil {
		return nil, err
	}
	out := []*CI{}
	for _, n := range numbers {
		ci, err := ciStatus(slug, n)
		if err != nil {
			return nil, err
		}
		out = append(out, ci)
	}
	return out, nil
}

var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[@-_]`)

// Logs returns the end of a failed GitHub Actions job's log.
func Logs(number int, check string, lines int) (string, error) {
	state, err := loadState(false)
	if err != nil {
		return "", err
	}
	slug, err := slugOf(state)
	if err != nil {
		return "", err
	}
	ci, err := ciStatus(slug, number)
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
