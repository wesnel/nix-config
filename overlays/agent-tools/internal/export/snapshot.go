package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// MaxSnapshot is the most a Space page's visualization may hold, the
// create_page_visualization tool's limit on its html.
const MaxSnapshot = 256 << 10

// Snapshot is the page with the documents Write put under dir in it, in
// place of the database, for a viewer without one: plans/*.json,
// github/status.json, meta/sync.json and today/digest.json, each its zero
// value where it is missing. They are encoded again from their types, with
// <, > and & escaped, so no text in them can end the script element that
// holds them. It refuses a snapshot over MaxSnapshot.
func Snapshot(dir string) ([]byte, error) {
	docs := struct {
		Plans  []Plan `json:"plans"`
		GitHub GitHub `json:"github"`
		Meta   Sync   `json:"meta"`
		Today  Today  `json:"today"`
	}{Plans: []Plan{}}
	for file, v := range map[string]any{"github/status.json": &docs.GitHub, "meta/sync.json": &docs.Meta,
		"today/digest.json": &docs.Today} {
		if err := readDoc(filepath.Join(dir, file), v); err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "plans"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var p Plan
		if err := readDoc(filepath.Join(dir, "plans", entry.Name()), &p); err != nil {
			return nil, err
		}
		docs.Plans = append(docs.Plans, p)
	}
	data, err := json.Marshal(docs) // escapes <, > and &
	if err != nil {
		return nil, err
	}
	marker := []byte("<!-- snapshot -->")
	if bytes.Count(agentplans.Page, marker) != 1 {
		return nil, fmt.Errorf("the page has no one place for a snapshot")
	}
	element := append(append([]byte(`<script type="application/json" id="snapshot">`), data...), "</script>"...)
	page := bytes.Replace(agentplans.Page, marker, element, 1)
	if len(page) > MaxSnapshot {
		return nil, fmt.Errorf("the snapshot is %d bytes, over the 256 KiB a Space visualization may hold", len(page))
	}
	return page, nil
}

// readDoc reads a document Write wrote into v, leaving v as it is where
// the file is missing.
func readDoc(file string, v any) error {
	data, err := readRegular(file, maxDoc)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}
