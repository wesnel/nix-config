package agentplans

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

const SpaceTitle = "Agent plans"

// SpaceConfigFile is operator-owned, protected by the guard's agent-tools
// configuration rule. Deployed builds bake ConfigHome in; there is no per-file
// environment override or command-line target override.
func SpaceConfigFile() string {
	return filepath.Join(hostpaths.ConfigHome(), "agent-tools", "agent-plans-space.json")
}

type SpaceTarget struct {
	PageID   string `json:"page_id"`
	BlockID  string `json:"block_id"`
	ThreadID string `json:"thread_id"`
}

var spaceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

func LoadSpaceTarget() (SpaceTarget, error) {
	var target SpaceTarget
	data, err := os.ReadFile(SpaceConfigFile())
	if err != nil {
		return target, fmt.Errorf("Space publication target is not configured by the operator: %w", err)
	}
	if err := strictJSON(data, &target); err != nil {
		return target, fmt.Errorf("invalid Space target configuration: %w", err)
	}
	if !spaceID.MatchString(target.PageID) || !spaceID.MatchString(target.BlockID) || !spaceID.MatchString(target.ThreadID) {
		return target, fmt.Errorf("Space target requires exact page_id, block_id and thread_id")
	}
	return target, nil
}

type SpaceVisualization struct {
	PageID       string `json:"page_id"`
	ReplaceBlock struct {
		BlockID      string `json:"block_id"`
		ExpectedHash string `json:"expected_hash"`
	} `json:"replace_block"`
	BaseSequence *int64 `json:"base_sequence,omitempty"`
	Title        string `json:"title"`
	HTML         string `json:"html"`
}

// SpaceOperation recognizes only the raw Pages namespace and the executor's
// normalized spelling, never a server-wide prefix or a fuzzy tool alias.
func SpaceOperation(name string) string {
	switch name {
	case "chatgpt_space__read_page", "chatgpt_space_read_page":
		return "read"
	case "chatgpt_space__create_page_visualization", "chatgpt_space_create_page_visualization":
		return "visualization"
	}
	return ""
}

// AuthorizeSpaceSnapshot validates a direct publisher request before it records
// the pending attempt. Its only write replaces the operator's exact owned block
// with the built-in renderer and safely encoded data, using an observed hash.
func AuthorizeSpaceSnapshot(input json.RawMessage) error {
	target, err := LoadSpaceTarget()
	if err != nil {
		return err
	}
	if state, err := loadSpaceState(SpaceStateFile()); err != nil || state.Pending != nil {
		return fmt.Errorf("Space publication needs readback reconciliation before another upload")
	}
	var args SpaceVisualization
	if err := exactKeys(input, "page_id", "replace_block", "base_sequence", "title", "html"); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return err
	}
	if err := exactKeys(raw["replace_block"], "block_id", "expected_hash"); err != nil {
		return err
	}
	if err := strictJSON(input, &args); err != nil {
		return fmt.Errorf("invalid snapshot arguments: %w", err)
	}
	if args.PageID != target.PageID || args.ReplaceBlock.BlockID != target.BlockID ||
		args.ReplaceBlock.ExpectedHash == "" || len(args.ReplaceBlock.ExpectedHash) > 500 || args.Title != SpaceTitle ||
		(args.BaseSequence != nil && *args.BaseSequence < 0) {
		return fmt.Errorf("snapshot must replace the configured Page and owned block with an observed hash")
	}
	return ValidateSnapshot([]byte(args.HTML))
}

func exactKeys(data []byte, allowed ...string) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	for key := range values {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return fmt.Errorf("Space request contains an unrecognized field")
		}
	}
	return nil
}

func strictJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
