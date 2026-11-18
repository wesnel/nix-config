package limavm

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

// RefreshLinks points the stable links at c's lima-mcp and at configFile,
// c's configuration in the store, which the relays read through them.
// Each link is made under a name of its own and renamed into place, so
// that concurrent refreshes, from lima-mcp and the operator's lima-dev,
// each leave a whole link.
func RefreshLinks(c *Config, configFile string) error {
	configFile, err := filepath.Abs(configFile)
	if err != nil {
		return err
	}
	for link, target := range map[string]string{c.ConfigLink(): configFile, c.BinLink(): c.LimaMCP} {
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			return err
		}
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		tmp := link + ".tmp-" + hex.EncodeToString(b[:])
		if err := os.Symlink(target, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, link); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}
