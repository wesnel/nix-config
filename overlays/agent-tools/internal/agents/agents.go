// Package agents reads agents.json, which names the operator's agents and
// what each one is: its guard role, and whether it is a user-level Claude
// Code agent. The guard reads it here, and the agents' Nix modules read
// the file itself, through agent-tools' passthru.agents.
package agents

import (
	_ "embed"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

//go:embed agents.json
var agentsJSON []byte

var file = func() (f struct {
	Roles  map[string]string `json:"roles"`
	Claude []string          `json:"claude"`
}) {
	if err := json.Unmarshal(agentsJSON, &f); err != nil {
		panic("agents: agents.json: " + err.Error())
	}
	return f
}()

// Roles are the agents with a guard role of their own: the role's name,
// by agent name. Each must be a role the guard knows, or it panics.
func Roles() map[string]string { return maps.Clone(file.Roles) }

// Claude are the user-level Claude Code agents the operator's
// configuration defines. The Claude module asserts it defines these and no
// others.
func Claude() []string { return slices.Clone(file.Claude) }

// Lookup is m's entry for the agent type agentType: the entry by that name,
// or, for a type a plugin namespaces as "plugin:name", the entry for name.
func Lookup[V any](m map[string]V, agentType string) (V, bool) {
	if v, ok := m[agentType]; ok {
		return v, true
	}
	if i := strings.LastIndexByte(agentType, ':'); i >= 0 {
		v, ok := m[agentType[i+1:]]
		return v, ok
	}
	var zero V
	return zero, false
}
