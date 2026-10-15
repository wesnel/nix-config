package gitenv

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	Isolate()
	os.Exit(m.Run())
}
