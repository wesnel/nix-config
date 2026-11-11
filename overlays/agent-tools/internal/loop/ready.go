package loop

import (
	"bufio"
	"fmt"
	"strings"
)

// ReadyResult records each successful transition, including partial progress.
type ReadyResult struct {
	PR      int    `json:"pr"`
	Ready   []int  `json:"ready"`
	Skipped string `json:"skipped,omitempty"`
}

// Ready asks the operator on /dev/tty before marking selected drafts ready.
// Repeating it skips PRs that are already ready, including after a partial failure.
func Ready(target string) (any, error) {
	plan, err := MergePreview(target)
	if err != nil {
		return nil, err
	}
	result := ReadyResult{PR: plan.PR, Ready: []int{}}
	var drafts []MergePlanPR
	for _, pr := range plan.PRs {
		if pr.IsDraft {
			drafts = append(drafts, pr)
		}
	}
	if len(drafts) == 0 {
		return result, nil
	}
	confirmed, err := confirmReady(drafts)
	if err != nil {
		return nil, err
	}
	if !confirmed {
		result.Skipped = "not confirmed"
		return result, nil
	}
	for i, pr := range plan.PRs {
		if !pr.IsDraft {
			continue
		}
		fresh, err := recheckMergePlan(plan)
		if err != nil {
			return result, err
		}
		if !fresh.PRs[i].IsDraft {
			continue
		}
		if err := markReady(pr); err != nil {
			return result, err
		}
		result.Ready = append(result.Ready, pr.Number)
	}
	return result, nil
}

func confirmReady(drafts []MergePlanPR) (bool, error) {
	tty, err := openTTY()
	if err != nil {
		return false, refuse("agent-pr ready needs /dev/tty (%v); run it from the operator's terminal", err)
	}
	defer tty.Close()
	fmt.Fprintln(tty, "\n=== Mark these drafts ready for review, bottom first:")
	for _, pr := range drafts {
		fmt.Fprintf(tty, "  #%d %s: %s (head %s)\n", pr.Number, pr.HeadRefName, pr.Title, pr.HeadRefOid)
	}
	fmt.Fprint(tty, "=== Mark ready? [y/N] ")
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && answer == "" {
		return false, fmt.Errorf("reading the answer: %w", err)
	}
	return strings.TrimSpace(answer) == "y", nil
}

func markReady(pr MergePlanPR) error {
	var data struct {
		MarkPullRequestReadyForReview struct {
			PullRequest *struct {
				ID      string
				IsDraft bool
			}
		}
	}
	err := graphQL(`mutation($id: ID!) {
		markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id isDraft } }
	}`, map[string]any{"id": pr.ID}, &data)
	if err != nil {
		return fmt.Errorf("marking #%d ready: %w", pr.Number, err)
	}
	changed := data.MarkPullRequestReadyForReview.PullRequest
	if changed == nil || changed.ID != pr.ID || changed.IsDraft {
		return fmt.Errorf("GitHub did not confirm #%d is ready", pr.Number)
	}
	return nil
}
