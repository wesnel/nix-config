package limaquic

import (
	"crypto/x509"
	"testing"
	"time"
)

func TestLeaves(t *testing.T) {
	// The clock a flow's leaves read, which the test moves on; x509 keeps
	// whole seconds.
	start := time.Now().Truncate(time.Second)
	now := start
	l, err := newLeaves(newAuthority(t).cert)
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return now }
	leaf := func() *x509.Certificate {
		t.Helper()
		minted, err := l.get(server)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(minted.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}

	first := leaf()
	if want := start.Add(24 * time.Hour); !first.NotAfter.Equal(want) {
		t.Fatalf("a leaf minted at %v expires %v, want %v", start, first.NotAfter, want)
	}
	now = start.Add(22 * time.Hour)
	if again := leaf(); again.SerialNumber.Cmp(first.SerialNumber) != 0 {
		t.Fatal("a leaf with two hours left was minted again")
	}
	now = start.Add(23*time.Hour + 30*time.Minute)
	renewed := leaf()
	if renewed.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatalf("at %v the leaf expiring %v was kept", now, first.NotAfter)
	}
	if want := now.Add(24 * time.Hour); !renewed.NotAfter.Equal(want) {
		t.Fatalf("the renewed leaf expires %v, want %v", renewed.NotAfter, want)
	}
}
