package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRepoCredential judges paths as git lists them in a repository, which
// may sit anywhere once checked out, against the credential paths the
// config lists.
func TestRepoCredential(t *testing.T) {
	listed := []string{
		"~/.ssh/id_", "~/.config/ngrok", "~/.aws/", "~/.netrc", "~/.docker/config.json",
		"~/.codex/auth.json", "~/.claude/.credentials.json", "~/.config/sops-nix/key.txt", "~/.cargo/credentials.toml",
		"/private/var/folders/*/*/T/secrets.d",
	}
	for _, c := range []struct {
		name, path string
		credential bool
	}{
		{"a .env file at the top", ".env", true},
		{"a named .env file deep down", "deploy/prod/.env.production", true},
		{"a file whose name only starts like .env", "tools/.envrc", false},
		{"a private key under .ssh", ".ssh/id_ed25519", true},
		{"a private key under a copied home", "dotfiles/home/.ssh/id_rsa", true},
		{"a public key under .ssh", ".ssh/id_rsa.pub", false},
		{"a file named like a key outside .ssh", "pkg/id_parse.go", false},
		{"a prefix entry's directory", ".config/ngrok/ngrok.yml", true},
		{"a directory named as a credential's last name alone", "tools/ngrok/run.go", false},
		{"a file in a listed directory", ".aws/credentials", true},
		{"a listed directory itself", "home/.aws", true},
		{"a directory only starting like a listed one", ".awsx/config", false},
		{"a listed file in the home directory", "nested/.netrc", true},
		{"a listed file in its directory", "fixtures/.docker/config.json", true},
		{"a glob credential outside the home directory, by its last name", "cache/secrets.d/age-keys.txt", true},
		{"a name only starting like that last name", "secrets.dx", false},
		// A file's own name alone, wherever it is, refuses what was copied
		// out of its directory.
		{"a listed file's name in another directory", "testdata/config.json", true},
		{"a private key at the top", "id_ed25519", true},
		{"a private key in keys", "keys/id_ed25519", true},
		{"a private key in ssh", "ssh/id_ed25519", true},
		{"an ngrok config at the top", "ngrok.yml", true},
		{"an ngrok config nested", "deploy/conf/ngrok-dev.yaml", true},
		{"a Codex login at the top", "auth.json", true},
		{"a Codex login nested", "backup/auth.json", true},
		{"a Claude login at the top", ".credentials.json", true},
		{"a Claude login nested", "x/y/.credentials.json", true},
		{"an age key at the top", "key.txt", true},
		{"an age key nested", "secrets/key.txt", true},
		{"a cargo login at the top", "credentials.toml", true},
		{"a cargo login nested", "ci/credentials.toml", true},
		{"a source file named like the ngrok config's directory", "pkg/tunnel/ngrok.go", false},
		{"a public key by its name alone", "keys/id_ed25519.pub", false},
		{"a name only starting like a listed file", "auth.json.example.md", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeHome(t)
			config := filepath.Join(t.TempDir(), "config.json")
			data, _ := json.Marshal(map[string]any{"guard": map[string]any{"credentialPaths": listed}})
			if err := os.WriteFile(config, data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_TOOLS_CONFIG", config)
			if got := IsRepoCredential(c.path); got != c.credential {
				t.Errorf("IsRepoCredential(%q) = %v, want %v", c.path, got, c.credential)
			}
		})
	}

	// With no credential paths at all, everything under the home directory
	// is one, and so is every path of a repository.
	t.Run("everything without a list", func(t *testing.T) {
		fakeHome(t)
		old := builtCredentialPaths
		builtCredentialPaths = nil
		t.Cleanup(func() { builtCredentialPaths = old })
		t.Setenv("AGENT_TOOLS_CONFIG", filepath.Join(t.TempDir(), "none.json"))
		if !IsRepoCredential("README.md") {
			t.Error("IsRepoCredential(README.md) = false with no credential paths")
		}
	})
}
