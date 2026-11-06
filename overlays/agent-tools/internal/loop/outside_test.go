package loop

import (
	"context"
	"encoding/json"
	"testing"
)

// withOutside is ctx with o answering for the outside the loop reaches
// under it, in place of GitHub, the terminal and the clock where o sets
// them.
func withOutside(ctx context.Context, o outside) context.Context {
	return context.WithValue(ctx, outsideKey{}, o)
}

// alsoOutside is ctx with change made to the outside it carries.
func alsoOutside(ctx context.Context, change func(o *outside)) context.Context {
	o, _ := ctx.Value(outsideKey{}).(outside)
	change(&o)
	return withOutside(ctx, o)
}

// Each call reaches the GitHub its context names, so two calls side by side
// can each be given a GitHub of their own.
func TestOutside(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	// as is a GitHub, faked since a test must not reach GitHub, whose
	// viewer is login.
	as := func(login string) outside {
		return outside{graphQL: func(_ context.Context, _ string, _ map[string]any, out any) error {
			data, _ := json.Marshal(map[string]any{"viewer": map[string]any{"login": login},
				"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}})
			return json.Unmarshal(data, out)
		}}
	}
	for _, login := range []string{"me", "you"} {
		t.Run(login, func(t *testing.T) {
			t.Parallel()
			repo, err := repository(withOutside(inRepo(t, dir), as(login)))
			if err != nil || repo.Slug != "o/r" || repo.Viewer != login {
				t.Errorf("the GitHub of a context whose viewer is %s: %+v, %v", login, repo, err)
			}
		})
	}
}
