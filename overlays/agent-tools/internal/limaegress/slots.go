package limaegress

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNoSlot is every slot being held.
var ErrNoSlot = errors.New("no free relay slot")

// Slots caps how many relays run at once, across processes.
type Slots interface {
	// Acquire takes a free slot and returns the function that releases
	// it. With none free it returns ErrNoSlot at once, without waiting.
	Acquire() (release func(), err error)
}

// FlockSlots is N slots, each a file slot-I in Dir held by flock(2), so
// one process's slot is released when it exits however it ends. Acquire
// creates Dir and the files as needed, readable by the user alone.
type FlockSlots struct {
	Dir string
	N   int
}

// Acquire takes the first free slot.
func (s FlockSlots) Acquire() (func(), error) {
	if s.N <= 0 {
		return nil, ErrNoSlot
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("relay slots: %w", err)
	}
	for i := range s.N {
		f, err := os.OpenFile(filepath.Join(s.Dir, fmt.Sprintf("slot-%d", i)), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, fmt.Errorf("relay slots: %w", err)
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		f.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("relay slots: locking slot %d: %w", i, err)
		}
	}
	return nil, ErrNoSlot
}
