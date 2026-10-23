package git

import (
	"context"
	"regexp"
	"strings"
	"time"

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

// ConfigTimeout bounds CheckConfig's git, which a FIFO in the repository
// can keep waiting: git reads the config files, and from 2.56 the
// alternates file, to list the config.
var ConfigTimeout = 10 * time.Second

// CheckConfig refuses to run git in dir (the process's directory when empty)
// if the repository's own or the worktree's config names a program, or if
// git cannot list that config within ConfigTimeout.
func CheckConfig(dir string) error {
	args := []string{"config", "--list", "--show-scope", "--name-only"}
	where := dir
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	} else if where = Dir; where == "" {
		where = "the current directory"
	}
	ctx, cancel := context.WithTimeout(context.Background(), ConfigTimeout)
	defer cancel()
	raw, err := BytesContext(ctx, nil, args...)
	if ctx.Err() != nil {
		return refusal.New("git took over %v to list the config of the repository at %s, so it is refused unchecked: "+
			"something there, such as a FIFO in its .git, keeps git waiting; stop, and hand the operator this message",
			ConfigTimeout, where)
	}
	out := strings.TrimSpace(string(raw))
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
			"machine and may have been written from inside a sandbox; stop, and hand the operator this message "+
			"to check where it came from and remove it", strings.Join(found, ", "))
	}
	return nil
}
