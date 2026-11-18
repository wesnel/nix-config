package limaquic

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestReadFirst(t *testing.T) {
	t.Run("a declared length is not allocated before its bytes arrive", func(t *testing.T) {
		// A first message declared at the cap, of which ten bytes come.
		body := append(protowire.AppendVarint(nil, maxFirstMessage), make([]byte, 10)...)
		// The least of a few runs, since other goroutines allocate too.
		least := ^uint64(0)
		for range 5 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, _, err := readFirst(bytes.NewReader(body))
			runtime.ReadMemStats(&after)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("readFirst of a cut message: %v, want unexpected EOF", err)
			}
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
		if least >= maxFirstMessage/4 {
			t.Fatalf("readFirst allocated %d bytes for 10 that came, want under %d", least, maxFirstMessage/4)
		}
	})

	t.Run("a whole message is returned with and without its prefix", func(t *testing.T) {
		message := []byte{0x0a, 1, 'x'}
		raw, got, err := readFirst(bytes.NewReader(append([]byte{3}, append(message, 'y')...)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, message) || !bytes.Equal(raw, []byte{3, 0x0a, 1, 'x'}) {
			t.Fatalf("readFirst = %x, %x; want 030a0178, 0a0178", raw, got)
		}
	})
}
