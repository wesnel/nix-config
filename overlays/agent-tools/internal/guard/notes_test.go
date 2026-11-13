package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// notesPaths makes the home plansHome does, with a digest and a brag file
// in its notes repository, a work directory and a link in it to the
// digests' year directory, and returns a replacer that puts their paths
// for NOTES (the notes repository), DIGEST, BRAG and WORKDIR, and the work
// directory itself.
func notesPaths(t *testing.T) (*strings.Replacer, string) {
	t.Helper()
	_, work := planLogPaths(t)
	notes := filepath.Join(filepath.Dir(work), "git", "notes")
	digest := filepath.Join(notes, "digests", "2026", "11-06-demo.org")
	brag := filepath.Join(notes, "brag", "2026.org")
	for _, f := range []string{digest, brag} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("* x\n"), 0o644)
	}
	os.Symlink(filepath.Dir(digest), filepath.Join(work, "week"))
	// A directory whose contents may hold any name.
	os.MkdirAll(filepath.Join(work, "forged"), 0o755)
	return strings.NewReplacer("NOTES", notes, "DIGEST", digest, "BRAG", brag, "WORKDIR", work), work
}

// A demo digest or a brag file is written by agent-plan alone, as a plan
// file is: no file tool and no shell write may change one in the notes
// repository, and the refusal names the commands to use. Files of the same
// names elsewhere, other files there, and reading them pass.
func TestNotesFile(t *testing.T) {
	patch := func(header, path string) map[string]string {
		return map[string]string{"command": "*** Begin Patch\n*** " + header + ": " + path + "\n@@\n+x\n*** End Patch\n"}
	}
	files := []struct {
		name  string
		deny  bool
		tool  string
		input any
	}{
		{"a digest", true, "Write", map[string]string{"file_path": "DIGEST", "content": "* Demo\n"}},
		{"a brag file", true, "Edit", map[string]string{"file_path": "BRAG", "old_string": "a", "new_string": "b"}},
		{"a new digest", true, "Write", map[string]string{"file_path": "NOTES/digests/2026/11-13-demo.org", "content": "* Demo\n"}},
		{"a new year's brag file", true, "Write", map[string]string{"file_path": "NOTES/brag/2027.org", "content": "* x\n"}},
		{"a digest", true, "apply_patch", patch("Update File", "DIGEST")},
		{"a digest through a link", true, "Write", map[string]string{"file_path": "WORKDIR/week/11-06-demo.org", "content": "x\n"}},
		{"a digest's name outside the notes", false, "Write", map[string]string{"file_path": "WORKDIR/digests/2026/11-06-demo.org", "content": "mine\n"}},
		{"another file among the digests", false, "Write", map[string]string{"file_path": "NOTES/digests/2026/notes.org", "content": "mine\n"}},
		{"another file among the brag files", false, "Write", map[string]string{"file_path": "NOTES/brag/README.org", "content": "mine\n"}},
	}
	for _, c := range files {
		t.Run(caseName(c.deny, c.tool+" of "+c.name), func(t *testing.T) {
			paths, work := notesPaths(t)
			data, _ := json.Marshal(c.input)
			input := []byte(paths.Replace(string(data)))
			reason := check(c.tool, input, baseline, work)
			if (reason != "") != c.deny {
				t.Errorf("%s %s: deny=%v, reason %q", c.tool, input, !c.deny, reason)
			}
			if reason != "" && (!saysInstead(reason) || !strings.Contains(reason, "`agent-plan digest") || !strings.Contains(reason, "`agent-plan brag")) {
				t.Errorf("%s: reason %q does not name the commands to use", c.tool, reason)
			}
		})
	}

	commands := []struct {
		deny         bool
		dir, command string
	}{
		{true, "WORKDIR", "echo x > DIGEST"},
		{true, "WORKDIR", "printf x >> BRAG"},
		{true, "WORKDIR", "cp /tmp/forged DIGEST"},
		{true, "WORKDIR", "rm BRAG"},
		{true, "WORKDIR", "sed -i '' s/a/b/ DIGEST"},
		{true, "WORKDIR", "echo x > week/11-06-demo.org"},
		{true, "NOTES", "tee brag/2026.org < /tmp/e"},
		{true, "WORKDIR", "cp /tmp/11-13-demo.org NOTES/digests/2026/"},
		{true, "WORKDIR", "git -C NOTES checkout HEAD~1 -- digests/2026/11-06-demo.org"},
		{true, "NOTES", "git restore brag"},
		// A directory's contents, a glob, an archive or a removal, any of
		// which may hold or take a digest or brag file.
		{true, "WORKDIR", "cp -R /tmp/forged/ NOTES/brag"},
		{true, "WORKDIR", "rsync -a /tmp/forged/ NOTES/brag/"},
		{true, "WORKDIR", "cp -R /tmp/scratch/2026 NOTES/digests/"},
		{true, "WORKDIR", "cp /tmp/backup/*.org NOTES/brag/"},
		{true, "WORKDIR", "tar -xf /tmp/x.tar -C NOTES/digests"},
		{true, "WORKDIR", "unzip /tmp/x.zip -d NOTES/brag"},
		{true, "WORKDIR", "ditto /tmp/backup NOTES/digests"},
		{true, "WORKDIR", "mv WORKDIR/forged NOTES/digests/2026"},
		{true, "WORKDIR", "rm -rf NOTES/brag"},
		{true, "WORKDIR", "rm -rf NOTES/digests/2026"},
		// Relative, as the guard refuses removing an absolute path under
		// /nix, where the Nix build's temporary directory is.
		{false, "NOTES", "rm digests/2026/notes.org"},
		{false, "WORKDIR", "tar -cf /tmp/b.tar -C NOTES digests"},
		{false, "WORKDIR", "agent-plan digest --date 2026-11-06 --text-file /tmp/digest.org"},
		{false, "WORKDIR", "agent-plan brag /tmp/p.org Q1 --pr o/r#1 --date 2026-11-05"},
		{false, "WORKDIR", "agent-plan brags"},
		{false, "WORKDIR", "cat DIGEST"},
		{false, "WORKDIR", "cp BRAG /tmp/copy.org"},
		{false, "WORKDIR", "echo x > /tmp/11-06-demo.org"},
		{false, "WORKDIR", "cp /tmp/notes.org NOTES/digests/2026/"},
		{false, "NOTES", "git restore todo.org"},
	}
	for _, c := range commands {
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			paths, _ := notesPaths(t)
			dir, command := paths.Replace(c.dir), paths.Replace(c.command)
			reason := judge(t, dir, command)
			if (reason != "") != c.deny {
				t.Errorf("%q in %q: deny=%v, reason %q", command, dir, !c.deny, reason)
			}
			if reason != "" && (!saysInstead(reason) || !strings.Contains(reason, "`agent-plan digest")) {
				t.Errorf("%q: reason %q does not name the commands to use", command, reason)
			}
		})
	}
}

// A guarded directory is known by its path: git restoring or checking out
// one missing from the working tree would write its files back from
// history.
func TestGuardedDirAbsent(t *testing.T) {
	for _, c := range []struct{ gone, command, want string }{
		{"brag", "git restore brag", "`agent-plan brag"},
		{"brag", "git checkout HEAD -- brag", "`agent-plan brag"},
		{"digests", "git restore digests", "`agent-plan digest"},
		{"digests/2026", "git restore digests/2026", "`agent-plan digest"},
		{"digests/2026", "git checkout HEAD~1 -- digests/2026", "`agent-plan digest"},
		{"agents", "git restore agents", "`agent-plan step"},
	} {
		t.Run(c.command+" with "+c.gone+" gone", func(t *testing.T) {
			paths, _ := notesPaths(t)
			notes := paths.Replace("NOTES")
			if err := os.RemoveAll(filepath.Join(notes, c.gone)); err != nil {
				t.Fatal(err)
			}
			if reason := judge(t, notes, c.command); !strings.Contains(reason, c.want) || !saysInstead(reason) {
				t.Errorf("%q with %s gone: reason %q, want one naming %s", c.command, c.gone, reason, c.want)
			}
		})
	}
}
