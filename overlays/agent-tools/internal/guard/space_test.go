package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// The Space publisher's state names the block it replaces, so no session
// writes it: by any name for it, before its directory exists or after.
func TestSpaceState(t *testing.T) {
	root := fakeHome(t)
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	file := agentplans.SpaceStateFile()
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Fatalf("the state's directory is there before the test makes it: %v", err)
	}
	write := func(path string) string {
		input, _ := json.Marshal(map[string]string{"file_path": path, "content": "{}"})
		return CheckBaseline("Write", input)
	}
	rel, _ := filepath.Rel(root, file)
	paths := []string{file, filepath.Dir(file) + "/./" + filepath.Base(file), filepath.Join(alias, rel)}
	if runtime.GOOS == "darwin" {
		paths = append(paths, filepath.Join(root, strings.ToUpper(rel)))
	}
	for _, path := range paths {
		if write(path) == "" {
			t.Errorf("Write %s before the directory exists", path)
		}
		if judge(t, root, "echo {} > "+path) == "" {
			t.Errorf("a redirection to %s", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	link, hard := filepath.Join(tmp, "link"), filepath.Join(tmp, "hard")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, hard); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, link, hard} {
		if write(path) == "" {
			t.Errorf("Write %s", path)
		}
	}
	if reason := judge(t, root, "git diff --output="+file); reason == "" {
		t.Error("git diff's output to the state")
	}
	// Beside it, a file of another name is written as any other.
	if other := filepath.Join(filepath.Dir(file), "out", "x.json"); write(other) != "" || judge(t, root, "echo {} > "+other) != "" {
		t.Errorf("a file beside the state: %s", write(other))
	}
}

// A command that writes a directory holding the state, such as an
// archive's extraction or a recursive copy, is refused; one that only
// reads it, or writes elsewhere, is not.
func TestSpaceStateDirectoryWrites(t *testing.T) {
	root := fakeHome(t)
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	dest, work := filepath.Dir(agentplans.SpaceStateFile()), t.TempDir()
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "linked-destination")
	if err := os.Symlink(dest, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, dir, command string
		deny               bool
	}{
		{"tar destination", work, "tar -xf fixture.tar -C " + dest, true},
		{"tar ancestor", work, "gtar xzf fixture.tgz --directory=" + filepath.Dir(dest), true},
		{"tar implicit destination", dest, "bsdtar -xf fixture.tar", true},
		{"tar linked destination", work, "tar -xf fixture.tar -C " + link, true},
		{"unzip destination", work, "unzip fixture.zip -d" + dest, true},
		{"copy ancestor", work, "cp -R fixture " + filepath.Dir(dest), true},
		{"rsync ancestor", work, "rsync -a fixture/ " + filepath.Dir(dest) + "/", true},
		{"tar listing", work, "tar -tf fixture.tar -C " + dest, false},
		{"unzip listing", dest, "unzip -l fixture.zip", false},
		{"extraction elsewhere", dest, "tar -xf fixture.tar -C " + work, false},
		{"copy elsewhere", work, "cp -R fixture " + filepath.Join(work, "safe"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if reason := judge(t, c.dir, c.command); (reason != "") != c.deny {
				t.Errorf("deny %v: %q", c.deny, reason)
			}
		})
	}
}
