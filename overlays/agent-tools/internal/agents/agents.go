// Package agents reads agents.json, which names the operator's agents and
// what each one is: its guard role, whether it is a user-level Claude Code
// agent or one of the ECA agents started by role, and the role its usage
// counts under. The guard and the usage report read it here, and the
// agents' Nix modules read the file itself, through agent-tools'
// passthru.agents.
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
	// ECA are the agents the ECA module defines for a coordinator to spawn
	// by role, from the Claude agents' definitions, and it asserts it
	// defines these and no others. The guard maps an ECA agent to its role
	// by name through Roles.
	ECA   []string          `json:"eca"`
	Usage map[string]string `json:"usage"`
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

// Usage are the agent types whose usage counts under a role of their own:
// the role, by agent name. Each must be one of spend.Roles, or the usage
// report panics.
func Usage() map[string]string { return maps.Clone(file.Usage) }

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
