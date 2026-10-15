package git

import (
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// programKeys are config keys that make git run a program, send data
// somewhere else, or read more config. In a repository's own config (which a
// sandboxed agent can write) they would run with the host's credentials when
// these tools call git. The hooks directory and the fsmonitor are switched
// off for every call instead, so a repository that uses them for itself
// still works. Hooks defined in config run even with core.hooksPath set to
// /dev/null, so those are refused here.
var programKeys = regexp.MustCompile(`^(` + strings.Join([]string{
	`core\.(sshcommand|gitproxy|askpass|editor|pager|attributesfile)`,
	`hook\..*`,
	`(pager|interactive)\..*`,
	`sequence\.editor`,
	`diff\.external`,
	`diff\..*\.(command|textconv)`,
	`merge\..*\.driver`,
	`filter\..*`,
	`credential(\..*)?`,
	`gpg\.(.*\.)?program`,
	`include\..*`,
	`includeif\..*`,
	`url\..*\.(pushinsteadof|insteadof)`,
	`remote\..*\.(receivepack|uploadpack|proxy|vcs)`,
	`http\..*`,
	`ssh\.variant`,
	`protocol\..*`,
}, "|") + `)$`)

// CheckConfig refuses to run git in dir (the process's directory when empty)
// if the repository's own or the worktree's config names a program.
func CheckConfig(dir string) error {
	args := []string{"config", "--list", "--show-scope", "--name-only"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := Run(args...)
	if err != nil {
		// Outside a repository there is nothing to check.
		return nil
	}
	var found []string
	for _, line := range strings.Split(out, "\n") {
		scope, key, ok := strings.Cut(line, "\t")
		if !ok || (scope != "local" && scope != "worktree") {
			continue
		}
		if programKeys.MatchString(strings.ToLower(key)) {
			found = append(found, scope+" "+key)
		}
	}
	if len(found) > 0 {
		return refusal.New("the repository's config sets %s, which would make git run a program on this "+
			"machine and may have been written from inside a sandbox; stop, and hand Wesley this message "+
			"to check where it came from and remove it", strings.Join(found, ", "))
	}
	return nil
}
