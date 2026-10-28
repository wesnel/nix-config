package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/repos"
)

// settings are the guard's own, from "guard" in the user's
// agent-tools/config.json.
type settings struct {
	// CredentialPaths are the absolute path prefixes of credentials, which
	// Claude Code's Read rules are made from too.
	CredentialPaths []string `json:"credentialPaths"`
	// ExcludedCommands are the patterns of the commands Claude Code's
	// sandbox excludes, its sandbox.excludedCommands, which the Claude Code
	// module writes here too (checkExcludedAlone).
	ExcludedCommands []string `json:"excludedCommands"`
}

const spaceAuthority = "Only the operator configures the Space publication target or reconciles its publication journal. " +
	"Use `plan-dash space prepare` for a snapshot, or put the proposed configuration or recovery in your report for the operator."

func spaceAuthorityFile(path string) bool {
	return Reaches(path, agentplans.SpaceConfigFile(), false) || Reaches(path, agentplans.SpaceStateFile(), false)
}

func checkSpaceAuthorityShell(name string, args, redirected []string, dir string) string {
	if shellWrites(name, args, redirected, dir, spaceAuthorityFile) {
		return spaceAuthority
	}
	if set("mv", "rm", "unlink", "trash", "rmdir")[name] {
		for _, value := range changedFiles(name, args, redirected) {
			path := expandPath(value, dir)
			if spaceAuthorityFile(path) || Reaches(agentplans.SpaceConfigFile(), path, true) || Reaches(agentplans.SpaceStateFile(), path, true) {
				return spaceAuthority
			}
		}
	}
	return ""
}

// builtCredentialPaths are wgn.home.llm.guard.credentialPaths as the
// package was built with them: overlays/agent-tools/default.nix generates
// the file that sets them. A path starting ~/ is under the home directory
// of wherever the guard runs, such as a sandbox guest's.
var builtCredentialPaths []string

// credentialPaths are the path prefixes of credentials: the config's, or
// without them the built-in ones, which stand in where the config is
// absent, as in a sandbox guest or on a host before its first switch. With neither, everything under the home directory is one.
func credentialPaths() []string {
	paths := loadSettings().CredentialPaths
	if len(paths) == 0 {
		paths = builtCredentialPaths
	}
	if len(paths) == 0 {
		return []string{home + "/"}
	}
	expanded := make([]string, len(paths))
	for i, p := range paths {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = home + "/" + rest
		}
		expanded[i] = p
	}
	return expanded
}

// agentToolsConfig reports whether path is agent-tools' config, which holds
// the guard's settings: the file repos.Path names, or anything in
// agent-tools' directory under the XDG config directory, as hostpaths has
// it or as the guard's environment does.
func agentToolsConfig(path string) bool {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		base = filepath.Join(home, ".config")
	}
	return path == filepath.Clean(repos.Path()) || inAgentTools(path, base, hostpaths.ConfigHome())
}

// inAgentTools reports whether path is in agent-tools' directory under one
// of bases.
func inAgentTools(path string, bases ...string) bool {
	for _, base := range bases {
		dir := filepath.Join(base, "agent-tools")
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// loadSettings reads the guard's settings; a config that is missing or
// unreadable gives none.
func loadSettings() settings {
	var file struct {
		Guard settings `json:"guard"`
	}
	if data, err := os.ReadFile(repos.Path()); err == nil {
		json.Unmarshal(data, &file)
	}
	return file.Guard
}
