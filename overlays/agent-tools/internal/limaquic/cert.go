package limaquic

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"sync"
	"time"
)

// leafLifetime bounds a minted leaf. A broker's leaves outlive it, since a
// broker serves for as long as its process runs, so a leaf with less than
// leafRenewal left is minted again for the next handshake.
const (
	leafLifetime = 24 * time.Hour
	leafRenewal  = time.Hour
)

// leaves mints, in memory, a server certificate for each name it is asked
// for, signed by the CA, and keeps it until it nears expiry. It is asked
// only for names the policy lists.
type leaves struct {
	ca  *x509.Certificate
	key crypto.Signer

	mu     sync.Mutex
	minted map[string]mintedLeaf

	// now is the clock the leaves are minted and renewed by.
	now func() time.Time
}

// mintedLeaf is a leaf with its expiry, which renewal reads.
type mintedLeaf struct {
	cert     *tls.Certificate
	notAfter time.Time
}

func newLeaves(ca tls.Certificate) (*leaves, error) {
	if len(ca.Certificate) == 0 {
		return nil, errors.New("limaquic: the CA has no certificate")
	}
	cert := ca.Leaf
	if cert == nil {
		var err error
		if cert, err = x509.ParseCertificate(ca.Certificate[0]); err != nil {
			return nil, err
		}
	}
	if !cert.IsCA {
		return nil, errors.New("limaquic: the CA certificate is not a CA's")
	}
	key, ok := ca.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("limaquic: the CA's key cannot sign")
	}
	return &leaves{ca: cert, key: key, minted: map[string]mintedLeaf{}, now: time.Now}, nil
}

func (l *leaves) get(name string) (*tls.Certificate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if leaf, ok := l.minted[name]; ok && now.Add(leafRenewal).Before(leaf.notAfter) {
		return leaf.cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	notAfter := now.Add(leafLifetime)
	if notAfter.After(l.ca.NotAfter) {
		notAfter = l.ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		DNSNames:     []string{name},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, l.ca, &key.PublicKey, l.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der, l.ca.Raw}, PrivateKey: key}
	l.minted[name] = mintedLeaf{cert: leaf, notAfter: notAfter}
	return leaf, nil
}
