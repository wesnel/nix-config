package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// WriteSnapshot reads the saved export, including retained last-good documents.
// It replaces file only after the complete HTML fits the Pages UTF-8 limit.
// Encoding the typed documents again is essential: Write emits unescaped JSON.
func WriteSnapshot(dir, file string) error {
	var snapshot struct {
		Plans  []Plan `json:"plans"`
		GitHub GitHub `json:"github"`
		Meta   Sync   `json:"meta"`
		Today  Today  `json:"today"`
	}
	snapshot.Plans = []Plan{}
	for _, doc := range []struct {
		path  string
		value any
	}{
		{"github/status.json", &snapshot.GitHub},
		{"meta/sync.json", &snapshot.Meta},
		{"today/digest.json", &snapshot.Today},
	} {
		if err := readSnapshotDoc(filepath.Join(dir, doc.path), doc.value); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "plans"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var p Plan
		if err := readSnapshotDoc(filepath.Join(dir, "plans", entry.Name()), &p); err != nil {
			return err
		}
		snapshot.Plans = append(snapshot.Plans, p)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	marker := []byte("<!-- snapshot -->")
	if bytes.Count(agentplans.Page, marker) != 1 {
		return fmt.Errorf("page has no unique snapshot insertion point")
	}
	block := append([]byte(`<script type="application/json" id="snapshot">`), data...)
	block = append(block, []byte("</script>")...)
	page := bytes.Replace(agentplans.Page, marker, block, 1)
	if err := agentplans.ValidateSnapshot(page); err != nil {
		return err
	}
	return writeFile(file, page)
}

func readSnapshotDoc(file string, value any) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDoc+1))
	if err != nil {
		return err
	}
	if len(data) > maxDoc || !utf8.Valid(data) {
		return fmt.Errorf("%s is oversized or invalid UTF-8", file)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%s must be a JSON object", file)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}
