// Command git-push-guard is a pre-push hook, run for every repository from
// the global git config, that refuses to publish a private GitHub
// repository's history in a public one.
package main

import (
	"fmt"
	"os"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/pushguard"
)

const usage = `usage: git-push-guard REMOTE URL < UPDATES

Run by git as a pre-push hook. Refuses the push when a commit it sends to a
public GitHub repository is history of a private GitHub repository that one
of this checkout's remotes fetches from. To push to one public repository
anyway: git -c ` + pushguard.AllowKey + `=OWNER/NAME push ...
`

func main() {
	if len(os.Args) != 3 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	updates, err := pushguard.Parse(os.Stdin)
	if err == nil {
		err = pushguard.Check(os.Args[2], updates, pushguard.GitHub())
	}
	if err != nil {
		os.Exit(cli.Report("git-push-guard", nil, err, 0, os.Stdout, os.Stderr))
	}
}
