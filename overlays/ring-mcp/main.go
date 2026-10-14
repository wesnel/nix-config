// Command ring-mcp is the MCP server a Pebble Index ring's phone app calls:
// capture writes down what was said, and ask answers a question with a
// read-only command. See the usage text.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

const usage = `ring-mcp: an MCP server for a Pebble Index ring.

  ring-mcp --token-file PATH --repo DIR --ask-command PATH [--listen HOST:PORT]
           [--inbox FILE] [--ask-timeout DURATION]

Serves POST /mcp. Every request must carry "Authorization: Bearer TOKEN",
where TOKEN is the token file's contents. capture appends an org entry to
--inbox in --repo and commits it, unsigned, creating the repository if it is
missing. ask passes the question on standard input to --ask-command and
returns what it prints.
`

func main() {
	fs := flag.NewFlagSet("ring-mcp", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	listen := fs.String("listen", "127.0.0.1:6291", "")
	tokenFile := fs.String("token-file", "", "")
	repo := fs.String("repo", "", "")
	inbox := fs.String("inbox", "inbox.org", "")
	askCommand := fs.String("ask-command", "", "")
	askTimeout := fs.Duration("ask-timeout", time.Minute, "")
	fs.Parse(os.Args[1:])

	if *tokenFile == "" || *repo == "" || *askCommand == "" {
		fs.Usage()
		os.Exit(2)
	}
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	token = bytes.TrimSpace(token)
	if len(token) == 0 {
		log.Fatalf("%s is empty", *tokenFile)
	}

	c := &capturer{repo: *repo, inbox: *inbox, now: time.Now}
	a := &asker{command: *askCommand, timeout: *askTimeout}
	s := &server{token: token, tools: []tool{
		{
			Name:        "capture",
			Description: "Write down a note, reminder, todo or idea.",
			InputSchema: textSchema("text", "What the user said, unchanged."),
			arg:         "text",
			run:         c.capture,
		},
		{
			Name:        "ask",
			Description: "Answer a question about the user's tickets, builds, pull requests, documentation or captured notes.",
			InputSchema: textSchema("question", "The question as the user said it."),
			Annotations: map[string]any{"readOnlyHint": true},
			arg:         "question",
			run:         a.ask,
		},
	}}

	mux := http.NewServeMux()
	mux.Handle("/mcp", s)
	log.Printf("listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
