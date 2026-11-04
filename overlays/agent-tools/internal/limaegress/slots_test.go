package limaegress

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestFlockSlots(t *testing.T) {
	slots := FlockSlots{Dir: t.TempDir(), N: 2}
	first, err := slots.Acquire()
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second, err := slots.Acquire()
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if _, err := slots.Acquire(); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("third Acquire: %v, want ErrNoSlot", err)
	}
	first()
	third, err := slots.Acquire()
	if err != nil {
		t.Fatalf("Acquire after a release: %v", err)
	}
	second()
	third()
}

func TestFlockSlotsRefusesNone(t *testing.T) {
	if _, err := (FlockSlots{Dir: t.TempDir(), N: 0}).Acquire(); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("Acquire with no slots: %v, want ErrNoSlot", err)
	}
}

func TestServeRefusesWithoutAFreeSlot(t *testing.T) {
	f := newFixture(t)
	slots := FlockSlots{Dir: t.TempDir(), N: 2}
	f.relay.Slots = slots
	for range 2 {
		release, err := slots.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	wait(t, done)
	if n := f.resolved.Load(); n != 0 {
		t.Errorf("resolved %d times, want none", n)
	}
	if line := f.log.String(); !strings.Contains(line, "verdict=refused") || !strings.Contains(line, "slot") {
		t.Errorf("log = %q, want a refusal for the slots", line)
	}
}

func TestServeHoldsItsSlotWhileOpen(t *testing.T) {
	f := newFixture(t)
	f.relay.Slots = FlockSlots{Dir: t.TempDir(), N: 1}
	first, firstDone := f.serve()
	if status, _ := connect(t, first, "example.com:443"); status != http.StatusOK {
		t.Fatalf("first connection: status = %d, want 200", status)
	}
	up := f.accept()
	second, secondDone := f.serve()
	if status, _ := connect(t, second, "example.com:443"); status != http.StatusForbidden {
		t.Fatalf("second connection while the first is open: status = %d, want 403", status)
	}
	wait(t, secondDone)
	up.Close()
	first.Close()
	wait(t, firstDone)
}

func TestServeReleasesItsSlot(t *testing.T) {
	f := newFixture(t)
	f.relay.Slots = FlockSlots{Dir: t.TempDir(), N: 1}
	for i := range 2 {
		guest, done := f.serve()
		if status, _ := connect(t, guest, "example.com:443"); status != http.StatusOK {
			t.Fatalf("connection %d: status = %d, want 200", i, status)
		}
		f.accept().Close()
		guest.Close()
		wait(t, done)
	}
}

func TestServeRefusesWithoutSlots(t *testing.T) {
	f := newFixture(t)
	f.relay.Slots = nil
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	wait(t, done)
}
