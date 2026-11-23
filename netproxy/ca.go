package netproxy

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

const maxCachedLeaves = 128

type cachedLeaf struct {
	host string
	cert *tls.Certificate
}

type inspectionCA struct {
	root *x509.Certificate
	key  *ecdsa.PrivateKey

	mu     sync.Mutex
	byHost map[string]*list.Element
	recent list.List // Front is most recently used.
}

func newInspectionCA() (*inspectionCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	root := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "Vibepit Session Inspection CA"},
		NotBefore:             now,
		NotAfter:              now.Add(30 * 24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	root, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &inspectionCA{root: root, key: key, byHost: make(map[string]*list.Element)}, nil
}

// host comes from the admitted CONNECT authority, independently of client SNI.
func (ca *inspectionCA) certificate(host string) (*tls.Certificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	now := time.Now()
	if now.Before(ca.root.NotBefore) || !now.Before(ca.root.NotAfter) {
		return nil, errors.New("inspection CA is outside its validity period")
	}
	elem := ca.byHost[host]
	if elem != nil {
		entry := elem.Value.(cachedLeaf)
		end := entry.cert.Leaf.NotAfter
		// A leaf capped by the CA expiry cannot be renewed for longer.
		if !now.Before(entry.cert.Leaf.NotBefore) && (end.After(now.Add(5*time.Minute)) || end.Equal(ca.root.NotAfter)) {
			ca.recent.MoveToFront(elem)
			return entry.cert, nil
		}
	}
	cert, err := ca.issueLeaf(host, now)
	if err != nil {
		return nil, err
	}
	if elem != nil {
		ca.recent.Remove(elem)
	} else if ca.recent.Len() == maxCachedLeaves {
		evicted := ca.recent.Remove(ca.recent.Back()).(cachedLeaf)
		delete(ca.byHost, evicted.host)
	}
	ca.byHost[host] = ca.recent.PushFront(cachedLeaf{host, cert})
	return cert, nil
}

func (ca *inspectionCA) issueLeaf(host string, now time.Time) (*tls.Certificate, error) {
	end := now.Add(24 * time.Hour)
	if end.After(ca.root.NotAfter) {
		end = ca.root.NotAfter
	}
	leaf := &x509.Certificate{
		NotBefore:   now,
		NotAfter:    end,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		leaf.IPAddresses = []net.IP{ip.AsSlice()}
	} else {
		leaf.DNSNames = []string{host}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca.root, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}
