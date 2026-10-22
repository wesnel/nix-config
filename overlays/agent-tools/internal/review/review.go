// Package review holds the reviewer's contract and the shape of its reply.
// The contract is what `git fresh-review` gives Codex and prints for a
// sub-agent, so a change to it changes every reviewer at once.
package review

import _ "embed"

// Contract is the reviewer's instructions.
//
//go:embed reviewer.md
var Contract string

// Schema is the JSON Schema of a reviewer's reply. Every property is
// required and optional ones are nullable, since Codex's --output-schema
// takes OpenAI's strict structured-output subset. Its routes' enum is
// routes.Known.
//
//go:embed findings.schema.json
var Schema []byte
