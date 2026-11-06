package env

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEnv(t *testing.T) {
	t.Parallel()

	t.Run("reads what it was built with", func(t *testing.T) {
		t.Parallel()
		e := New("/work", "A=1", "B=two")
		if got := e.Get("A"); got != "1" {
			t.Errorf("Get(A) = %q, want 1", got)
		}
		if got, ok := e.Lookup("B"); !ok || got != "two" {
			t.Errorf("Lookup(B) = %q, %v, want two, true", got, ok)
		}
		if e.Dir() != "/work" {
			t.Errorf("Dir() = %q, want /work", e.Dir())
		}
	})

	t.Run("an unset name is empty and not found", func(t *testing.T) {
		t.Parallel()
		e := New("/work", "A=1", "EMPTY=")
		if _, ok := e.Lookup("MISSING"); ok {
			t.Error("Lookup(MISSING) found a variable")
		}
		if got, ok := e.Lookup("EMPTY"); !ok || got != "" {
			t.Errorf("Lookup(EMPTY) = %q, %v, want empty and set", got, ok)
		}
		if got := e.Get("MISSING"); got != "" {
			t.Errorf("Get(MISSING) = %q, want empty", got)
		}
	})

	t.Run("a value keeps its equals signs", func(t *testing.T) {
		t.Parallel()
		if got := New("", "A=b=c").Get("A"); got != "b=c" {
			t.Errorf("Get(A) = %q, want b=c", got)
		}
	})

	t.Run("the last of a repeated name wins", func(t *testing.T) {
		t.Parallel()
		if got := New("", "A=1", "A=2").Get("A"); got != "2" {
			t.Errorf("Get(A) = %q, want 2", got)
		}
	})

	t.Run("Set replaces and does not change the receiver", func(t *testing.T) {
		t.Parallel()
		base := New("/work", "A=1", "B=2")
		changed := base.Set("A=9", "C=3")
		if got := changed.Environ(); !slices.Equal(got, []string{"B=2", "A=9", "C=3"}) {
			t.Errorf("Set(A=9, C=3).Environ() = %v, want [B=2 A=9 C=3]", got)
		}
		if got := base.Get("A"); got != "1" {
			t.Errorf("the receiver's A became %q after Set", got)
		}
	})

	t.Run("Without removes every variable named", func(t *testing.T) {
		t.Parallel()
		e := New("", "A=1", "B=2", "A=3", "C=4").Without("A", "C")
		if got := e.Environ(); !slices.Equal(got, []string{"B=2"}) {
			t.Errorf("Without(A, C).Environ() = %v, want [B=2]", got)
		}
	})

	t.Run("In changes only the directory", func(t *testing.T) {
		t.Parallel()
		base := New("/one", "A=1")
		moved := base.In("/two")
		if moved.Dir() != "/two" || moved.Get("A") != "1" {
			t.Errorf("In(/two) = dir %q, A %q, want /two and 1", moved.Dir(), moved.Get("A"))
		}
		if base.Dir() != "/one" {
			t.Errorf("the receiver's directory became %q after In", base.Dir())
		}
	})

	t.Run("Environ is a copy", func(t *testing.T) {
		t.Parallel()
		e := New("", "A=1")
		got := e.Environ()
		got[0] = "A=changed"
		if e.Get("A") != "1" {
			t.Errorf("modifying the result of Environ changed the Env: A is %q", e.Get("A"))
		}
	})

	t.Run("a context carries its Env", func(t *testing.T) {
		t.Parallel()
		ctx := With(t.Context(), New("/work", "A=1"))
		if got := From(ctx); got.Get("A") != "1" || got.Dir() != "/work" {
			t.Errorf("From = dir %q, A %q, want /work and 1", got.Dir(), got.Get("A"))
		}
	})

	t.Run("Command runs the program of the Env's PATH in its directory", func(t *testing.T) {
		t.Parallel()
		bin, work := t.TempDir(), t.TempDir()
		script := "#!/bin/sh\necho \"$(pwd -P) $SAYS\"\n"
		if err := os.WriteFile(filepath.Join(bin, "envtest-prog"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		// A relative directory first, which holds no such program.
		ctx := With(t.Context(), New(work, "PATH=relative:"+bin+":/usr/bin:/bin", "SAYS=hello"))
		out, err := Command(ctx, "envtest-prog").Output()
		if err != nil {
			t.Fatalf("Command(envtest-prog): %v", err)
		}
		real, err := filepath.EvalSymlinks(work)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != real+" hello" {
			t.Errorf("the program printed %q, want %q", got, real+" hello")
		}
	})

	// exec.LookPath refuses to run a program it finds in a relative directory
	// of the PATH, an empty entry included, with exec.ErrDot, and never goes on
	// to a later directory's program of the same name.
	t.Run("a program found in a relative PATH entry is refused with ErrDot", func(t *testing.T) {
		t.Parallel()
		bin := t.TempDir()
		for _, dir := range []string{bin, filepath.Join(bin, "later")} {
			os.MkdirAll(dir, 0o755)
			if err := os.WriteFile(filepath.Join(dir, "envtest-dot"), []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(wd, bin)
		if err != nil {
			t.Skipf("no relative path from %s to %s: %v", wd, bin, err)
		}
		e := New(t.TempDir(), "PATH="+rel+":"+filepath.Join(bin, "later"))
		if path, err := e.LookPath("envtest-dot"); !errors.Is(err, exec.ErrDot) {
			t.Errorf("LookPath found %q, %v in a relative entry; want exec.ErrDot", path, err)
		}
		if err := Command(With(t.Context(), e), "envtest-dot").Run(); !errors.Is(err, exec.ErrDot) {
			t.Errorf("Command ran a program found in a relative entry, or failed otherwise: %v; want exec.ErrDot", err)
		}
	})

	t.Run("Command finds nothing the Env's PATH lacks", func(t *testing.T) {
		t.Parallel()
		ctx := With(t.Context(), New(t.TempDir(), "PATH="+t.TempDir()))
		if err := Command(ctx, "sh", "-c", "true").Run(); !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("Command(sh) with a PATH that lacks it ran, or failed otherwise: %v", err)
		}
		if _, err := New("", "PATH="+t.TempDir()).LookPath("sh"); !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("LookPath(sh) with a PATH that lacks it: %v, want exec.ErrNotFound", err)
		}
	})

	t.Run("Command gives an empty Env nothing of this process's", func(t *testing.T) {
		t.Parallel()
		if _, ok := os.LookupEnv("HOME"); !ok {
			t.Skip("HOME is unset in this process")
		}
		out, err := Command(With(t.Context(), New("")), "/bin/sh", "-c", "echo \"HOME=${HOME-unset}\"").Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != "HOME=unset" {
			t.Errorf("a program of an empty Env saw %q, want HOME=unset", got)
		}
	})

	t.Run("a context with none gives the process environment", func(t *testing.T) {
		t.Parallel()
		got := From(context.Background())
		want, _ := os.LookupEnv("PATH")
		if want == "" {
			t.Skip("PATH is unset in this process")
		}
		if got.Get("PATH") != want {
			t.Errorf("From(background).Get(PATH) = %q, want the process's %q", got.Get("PATH"), want)
		}
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if got.Dir() != wd {
			t.Errorf("From(background).Dir() = %q, want the process's %q", got.Dir(), wd)
		}
	})
}
