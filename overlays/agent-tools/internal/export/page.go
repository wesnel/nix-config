package export

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// WritePage puts agentplans.Page at file where it differs, and reports
// whether it differs from the page published, whose hash published holds.
func WritePage(file, published string) (bool, error) {
	if current, err := os.ReadFile(file); err != nil || !bytes.Equal(current, agentplans.Page) {
		if err := writeFile(file, agentplans.Page); err != nil {
			return false, err
		}
	}
	recorded, err := os.ReadFile(published)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return strings.TrimSpace(string(recorded)) != agentplans.PageHash(agentplans.Page), nil
}

// MarkPublished records the page at file as the one published, in
// published.
func MarkPublished(file, published string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return writeFile(published, []byte(agentplans.PageHash(data)+"\n"))
}

// writeFile replaces file with data, 0600, by rename.
func writeFile(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
