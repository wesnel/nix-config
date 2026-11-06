package limavm

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/limaegress"
)

// caLifetime is long, so that the guest's trust, set when the instance is
// created, does not lapse under a VM the operator keeps; the name
// constraints, not the lifetime, bound what the CA can vouch for.
const caLifetime = 10 * 365 * 24 * time.Hour

// EnsureCA makes the broker's CA, an ECDSA P-256 key and a certificate
// whose name constraints permit only the QUIC servers, unless certFile
// already holds one. It writes the key 0600 and creates their directory
// 0700. A CA already there is kept when it permits exactly those servers,
// and otherwise refused: the guest trusts the CA it was created with.
// Callers in other processes, lima-mcp and lima-dev, are serialised by a
// flock on ca.lock beside certFile, under which the CA is looked for
// again, so two never write a key and a certificate of different CAs.
// Waiting for the lock ends with ctx, whose error it then returns.
func EnsureCA(ctx context.Context, certFile, keyFile string, quicServers []limaegress.Name) error {
	dir := filepath.Dir(certFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "ca.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("the broker CA's lock: %w", err)
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("the broker CA's lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the broker CA's lock: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	servers := make([]string, len(quicServers))
	for i, s := range quicServers {
		servers[i] = string(s)
	}
	return ensureCA(certFile, keyFile, servers)
}

// ensureCA is EnsureCA under its lock.
func ensureCA(certFile, keyFile string, servers []string) error {
	if _, err := os.Lstat(certFile); err == nil {
		ca, err := LoadCA(certFile, keyFile)
		if err != nil {
			return err
		}
		have, want := slices.Sorted(slices.Values(ca.Leaf.PermittedDNSDomains)), slices.Sorted(slices.Values(servers))
		if !slices.Equal(have, want) || !ca.Leaf.PermittedDNSDomainsCritical {
			return fmt.Errorf("the broker CA %s permits %v, not the configured QUIC servers %v: delete the instance and the CA's two files, then start again", certFile, have, want)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "lima-mcp private-dial broker"},
		NotBefore:                   now.Add(-5 * time.Minute),
		NotAfter:                    now.Add(caLifetime),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         servers,
		// A permitted DNS name leaves the other name forms unconstrained,
		// so every IP address, URI and email is excluded.
		ExcludedIPRanges:       []*net.IPNet{{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, {IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}},
		ExcludedURIDomains:     []string{""},
		ExcludedEmailAddresses: []string{""},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	// The key first, so that a certificate is never there without it.
	if err := writeNew(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return err
	}
	return writeNew(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// writeNew writes data to a temporary file beside name, 0600, and renames
// it into place.
func writeNew(name string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}

// LoadCA reads the broker's CA, with its parsed certificate in Leaf.
func LoadCA(certFile, keyFile string) (tls.Certificate, error) {
	ca, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("the broker CA: %w", err)
	}
	if ca.Leaf == nil || !ca.Leaf.IsCA {
		return tls.Certificate{}, fmt.Errorf("the broker CA %s is not a CA certificate", certFile)
	}
	return ca, nil
}
