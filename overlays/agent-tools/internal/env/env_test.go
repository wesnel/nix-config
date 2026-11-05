package env

import (
	"context"
	"os"
	"slices"
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
