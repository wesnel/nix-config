package limavm

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/limaegress"
)

func names(servers []string) []limaegress.Name {
	out := make([]limaegress.Name, len(servers))
	for i, s := range servers {
		out[i] = limaegress.Name(s)
	}
	return out
}

func TestEnsureCALock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "_mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Another process's hold on the lock, as lima-dev's would be.
	held, err := os.OpenFile(filepath.Join(dir, "ca.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- EnsureCA(ctx, filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"), names([]string{"us.quic.connect-endpoint.ngrok.com"}))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v; want the context's deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureCA still waits for the lock after its context ended")
	}
}

// leafFor signs a server certificate for name with the CA, as the broker
// does, so the test can ask x509 whether the guest would accept it.
func leafFor(t *testing.T, ca *x509.Certificate, caKey any, name string) *x509.Certificate {
	t.Helper()
	return leafWith(t, ca, caKey, name, func(*x509.Certificate) {})
}

// leafWith is leafFor with other names added by edit.
func leafWith(t *testing.T, ca *x509.Certificate, caKey any, name string, edit func(*x509.Certificate)) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	edit(template)
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func TestEnsureCA(t *testing.T) {
	servers := []string{"us.quic.connect-endpoint.ngrok.com", "eu.quic.connect-endpoint.ngrok.com"}
	dir := filepath.Join(t.TempDir(), "_mcp")
	certFile, keyFile := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key")
	if err := EnsureCA(t.Context(), certFile, keyFile, names(servers)); err != nil {
		t.Fatal(err)
	}
	ca, err := LoadCA(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	cert := ca.Leaf

	t.Run("is a P-256 CA constrained to the QUIC servers", func(t *testing.T) {
		if !cert.IsCA || !cert.PermittedDNSDomainsCritical {
			t.Fatalf("IsCA %v, constraints critical %v", cert.IsCA, cert.PermittedDNSDomainsCritical)
		}
		if !slices.Equal(cert.PermittedDNSDomains, servers) {
			t.Fatalf("permitted %v; want %v", cert.PermittedDNSDomains, servers)
		}
		if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() {
			t.Fatalf("key %T", cert.PublicKey)
		}
	})

	t.Run("the key is the user's alone", func(t *testing.T) {
		for _, f := range []string{keyFile, dir} {
			info, err := os.Stat(f)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s has mode %v", f, info.Mode().Perm())
			}
		}
	})

	t.Run("the guest accepts a leaf for a server and no other name", func(t *testing.T) {
		roots := x509.NewCertPool()
		roots.AddCert(cert)
		for name, want := range map[string]bool{servers[0]: true, "api.ngrok.com": false, "us.h2.connect-endpoint.ngrok.com": false} {
			_, err := leafFor(t, cert, ca.PrivateKey, name).Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
			if (err == nil) != want {
				t.Errorf("leaf for %s: err %v; want accepted %v", name, err, want)
			}
		}
	})

	t.Run("the guest refuses a leaf for a server that also names an IP address, URI or email", func(t *testing.T) {
		roots := x509.NewCertPool()
		roots.AddCert(cert)
		uri, _ := url.Parse("https://us.quic.connect-endpoint.ngrok.com/")
		for name, edit := range map[string]func(*x509.Certificate){
			"IPv4":  func(c *x509.Certificate) { c.IPAddresses = []net.IP{net.IPv4(192, 168, 5, 2)} },
			"IPv6":  func(c *x509.Certificate) { c.IPAddresses = []net.IP{net.ParseIP("::1")} },
			"URI":   func(c *x509.Certificate) { c.URIs = []*url.URL{uri} },
			"email": func(c *x509.Certificate) { c.EmailAddresses = []string{"a@us.quic.connect-endpoint.ngrok.com"} },
		} {
			leaf := leafWith(t, cert, ca.PrivateKey, servers[0], edit)
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: servers[0]}); err == nil {
				t.Errorf("a leaf with an %s accepted", name)
			}
		}
	})

	t.Run("keeps a CA that matches", func(t *testing.T) {
		before, _ := os.ReadFile(certFile)
		if err := EnsureCA(t.Context(), certFile, keyFile, names([]string{servers[1], servers[0]})); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(certFile)
		if !bytes.Equal(before, after) {
			t.Fatal("a matching CA was replaced")
		}
	})

	t.Run("refuses a CA for other servers", func(t *testing.T) {
		err := EnsureCA(t.Context(), certFile, keyFile, names(servers[:1]))
		if err == nil || !strings.Contains(err.Error(), "eu.quic.connect-endpoint.ngrok.com") {
			t.Fatalf("err = %v; want a refusal naming the CA's other server", err)
		}
	})

	t.Run("refuses a certificate without its key", func(t *testing.T) {
		d := t.TempDir()
		data, _ := os.ReadFile(certFile)
		if err := os.WriteFile(filepath.Join(d, "ca.pem"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := EnsureCA(t.Context(), filepath.Join(d, "ca.pem"), filepath.Join(d, "ca.key"), names(servers)); err == nil {
			t.Fatal("a certificate without its key was accepted")
		}
	})

	t.Run("concurrent callers leave one CA whose key matches", func(t *testing.T) {
		// lima-mcp and the operator's lima-dev may both make the CA.
		for round := range 20 {
			d := filepath.Join(t.TempDir(), "_mcp")
			cert, key := filepath.Join(d, "ca.pem"), filepath.Join(d, "ca.key")
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for range 8 {
				wg.Go(func() {
					if err := EnsureCA(t.Context(), cert, key, names(servers)); err != nil {
						errs <- err
					}
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatalf("round %d: a caller failed: %v", round, err)
			}
			if _, err := LoadCA(cert, key); err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	})
}
