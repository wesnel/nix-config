package agentplans

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactMirrorTargets includes every session's bound Artifact and the
// aggregate's configured and recorded destinations. Corrupt records refuse
// classification because an unreadable destination cannot be ruled out.
func ArtifactMirrorTargets() ([]string, error) {
	targets, err := LoadTargets()
	if err != nil {
		return nil, err
	}
	var result []string
	if targets.ClaudePage != "" {
		result = append(result, targets.ClaudePage)
	}
	entries, err := os.ReadDir(MirrorDir())
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		stem := strings.TrimSuffix(name, ".json")
		if name != "aggregate.json" && name != "aggregate-mirror.json" && !(len(stem) == 64 && strings.Trim(stem, "0123456789abcdef") == "" && name == stem+".json") {
			continue
		}
		page, err := artifactMirrorTarget(name)
		if err != nil {
			return nil, err
		}
		if page != "" {
			result = append(result, page)
		}
	}
	return result, nil
}

func artifactMirrorTarget(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(MirrorDir(), name))
	if err != nil {
		return "", err
	}
	if name == "aggregate.json" {
		var owner aggregateOwner
		if err := json.Unmarshal(data, &owner); err != nil {
			return "", err
		}
		if owner.Target != "" && !pageURL.MatchString(owner.Target) {
			return "", errors.New("invalid aggregate Artifact target")
		}
		return owner.Target, nil
	}
	var m Mirror
	if err := json.Unmarshal(data, &m); err != nil {
		return "", err
	}
	if name == "aggregate-mirror.json" {
		if m.Scope != "aggregate" || m.File != filepath.Join(MirrorDir(), "aggregate.html") || m.Backend != "claude" {
			return "", errors.New("invalid aggregate mirror identity")
		}
	} else if name != PageHash([]byte(m.Session))+".json" || m.File != filepath.Join(MirrorDir(), PageHash([]byte(m.Session))+".html") {
		return "", errors.New("invalid session mirror identity")
	}
	switch {
	case m.Backend == "space" && strings.HasPrefix(m.Session, "codex:") && (m.Page == "" || spacePage.MatchString(m.Page)):
		return "", nil
	case m.Backend == "claude" && strings.HasPrefix(m.Session, "claude:") && (m.Page == "" || pageURL.MatchString(m.Page)):
		return m.Page, nil
	default:
		return "", errors.New("invalid mirror backend or destination")
	}
}
