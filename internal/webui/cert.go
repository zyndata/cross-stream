package webui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
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
	"strings"
	"time"
)

// Self-signed certificate files in the data directory.
const (
	CertFile = "web.crt"
	KeyFile  = "web.key"
)

// LoadCert loads certFile/keyFile when given, else the self-signed pair in
// dataDir (created on first use: ECDSA P-256, 10 years, SANs localhost,
// 127.0.0.1, ::1). created reports a new certificate.
func LoadCert(certFile, keyFile, dataDir string, now time.Time) (cert tls.Certificate, created bool, err error) {
	if certFile != "" {
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return tls.Certificate{}, false, fmt.Errorf("web certificate: %w", err)
		}
		return cert, false, nil
	}
	certPath, keyPath := filepath.Join(dataDir, CertFile), filepath.Join(dataDir, KeyFile)
	cert, err = tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		return cert, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tls.Certificate{}, false, fmt.Errorf("web certificate: %w", err)
	}
	certPEM, keyPEM, err := selfSigned(now)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, false, fmt.Errorf("web certificate: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, false, fmt.Errorf("web certificate: %w", err)
	}
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	return cert, true, err
}

// Fingerprint is the SHA-256 of the leaf certificate, colon-separated hex
// (what browsers show).
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func selfSigned(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("web key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, fmt.Errorf("web serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "CrossStream panel", Organization: []string{"CrossStream"}},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("web certificate: %w", err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("web key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), nil
}
