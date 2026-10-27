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
	`core\.(sshcommand|gitproxy|askpass|editor|pager|attributesfile|alternaterefscommand)`,
	`hook\..*`,
	`(pager|interactive)\..*`,
	`sequence\.editor`,
	`diff\.external`,
	`diff\..*\.(command|textconv)`,
	`merge\..*\.driver`,
	`trailer\..*\.(cmd|command)`,
	`filter\..*`,
	`credential(\..*)?`,
	`gpg\.(.*\.)?program`,
	`gpg\.ssh\.defaultkeycommand`,
	`include\..*`,
	`includeif\..*`,
	`url\..*\.(pushinsteadof|insteadof)`,
	`remote\..*\.(receivepack|uploadpack|proxy|vcs)`,
	`http\..*`,
	`ssh\.variant`,
	`protocol\..*`,
}, "|") + `)$`)

// bangUpdate reports whether key and value are a submodule's update set to
// a command, `!CMD`, which git submodule update runs in a shell. Its other
// values (checkout, rebase, merge, none) run nothing.
func bangUpdate(key, value string) bool {
	k := strings.ToLower(key)
	return strings.HasPrefix(k, "submodule.") && strings.HasSuffix(k, ".update") &&
		strings.HasPrefix(strings.TrimSpace(value), "!")
}

// ConfigTimeout bounds CheckConfig's git, which a FIFO in the repository
// can keep waiting: git reads the config files, and from 2.56 the
// alternates file, to list the config.
var ConfigTimeout = 10 * time.Second

// CheckConfig refuses to run git in dir (the process's directory when empty)
// if the repository's own or the worktree's config names a program, or if
// git cannot list that config within ConfigTimeout.
func CheckConfig(dir string) error {
	args := []string{"config", "--list", "--show-scope", "-z"}
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
	if err != nil {
		// Outside a repository there is nothing to check.
		return nil
	}
	// With -z each entry is its scope, then its key and value: NUL after
	// each, and a newline between key and value.
	fields := strings.Split(string(raw), "\x00")
	var found []string
	for i := 0; i+1 < len(fields); i += 2 {
		scope := fields[i]
		key, value, _ := strings.Cut(fields[i+1], "\n")
		if scope != "local" && scope != "worktree" {
			continue
		}
		if programKeys.MatchString(strings.ToLower(key)) || bangUpdate(key, value) {
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
