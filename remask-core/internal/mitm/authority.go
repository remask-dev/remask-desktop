package mitm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	certificateFilename = "remask-ca.pem"
	privateKeyFilename  = "remask-ca-key.pem"
)

// Authority owns the local root certificate used to inspect configured HTTPS
// upstreams. Leaf certificates are short lived and cached only in memory.
type Authority struct {
	mu              sync.Mutex
	certificate     *x509.Certificate
	privateKey      *ecdsa.PrivateKey
	certificatePath string
	privateKeyPath  string
	leaves          map[string]tls.Certificate
}

type Status struct {
	Ready           bool   `json:"ready"`
	CertificatePath string `json:"certificate_path,omitempty"`
	Fingerprint     string `json:"fingerprint_sha256"`
}

func NewAuthority(dataDir string) (*Authority, error) {
	authority := &Authority{leaves: make(map[string]tls.Certificate)}
	if strings.TrimSpace(dataDir) == "" {
		certificate, key, err := generateRoot()
		if err != nil {
			return nil, err
		}
		authority.certificate, authority.privateKey = certificate, key
		return authority, nil
	}

	certificateDir := filepath.Join(dataDir, "certificates")
	if err := os.MkdirAll(certificateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create certificate directory: %w", err)
	}
	authority.certificatePath = filepath.Join(certificateDir, certificateFilename)
	authority.privateKeyPath = filepath.Join(certificateDir, privateKeyFilename)
	if err := authority.loadOrCreate(); err != nil {
		return nil, err
	}
	return authority, nil
}

func (a *Authority) CertificateFor(host string) (tls.Certificate, error) {
	host = normalizeHost(host)
	if host == "" {
		return tls.Certificate{}, errors.New("certificate host is empty")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if certificate, ok := a.leaves[host]; ok {
		return certificate, nil
	}
	certificate, err := a.generateLeaf(host)
	if err != nil {
		return tls.Certificate{}, err
	}
	a.leaves[host] = certificate
	return certificate, nil
}

func (a *Authority) RootCertificatePEM() []byte {
	if a == nil || a.certificate == nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.certificate.Raw})
}

func (a *Authority) Status() Status {
	if a == nil || a.certificate == nil {
		return Status{}
	}
	digest := sha256.Sum256(a.certificate.Raw)
	encoded := strings.ToUpper(hex.EncodeToString(digest[:]))
	parts := make([]string, 0, len(encoded)/2)
	for index := 0; index < len(encoded); index += 2 {
		parts = append(parts, encoded[index:index+2])
	}
	return Status{Ready: true, CertificatePath: a.certificatePath, Fingerprint: strings.Join(parts, ":")}
}

func (a *Authority) loadOrCreate() error {
	certificatePEM, certificateErr := os.ReadFile(a.certificatePath)
	privateKeyPEM, keyErr := os.ReadFile(a.privateKeyPath)
	if certificateErr == nil && keyErr == nil {
		certificate, key, err := parseRoot(certificatePEM, privateKeyPEM)
		if err != nil {
			return fmt.Errorf("load local certificate authority: %w", err)
		}
		a.certificate, a.privateKey = certificate, key
		return nil
	}
	if !errors.Is(certificateErr, os.ErrNotExist) && certificateErr != nil {
		return certificateErr
	}
	if !errors.Is(keyErr, os.ErrNotExist) && keyErr != nil {
		return keyErr
	}
	if certificateErr == nil || keyErr == nil {
		return errors.New("local certificate authority is incomplete; both certificate and private key are required")
	}

	certificate, key, err := generateRoot()
	if err != nil {
		return err
	}
	certificatePEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	privateKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeAtomic(a.certificatePath, certificatePEM, 0o644); err != nil {
		return fmt.Errorf("write local CA certificate: %w", err)
	}
	if err := writeAtomic(a.privateKeyPath, privateKeyPEM, 0o600); err != nil {
		return fmt.Errorf("write local CA private key: %w", err)
	}
	a.certificate, a.privateKey = certificate, key
	return nil
}

func generateRoot() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Remask Local Privacy Proxy", Organization: []string{"Remask"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	return certificate, key, err
}

func parseRoot(certificatePEM, privateKeyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certificateBlock, _ := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" {
		return nil, nil, errors.New("invalid certificate PEM")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(privateKeyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("invalid private key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("private key is not ECDSA")
	}
	if !certificate.IsCA {
		return nil, nil, errors.New("certificate is not a CA")
	}
	publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || publicKey.X.Cmp(key.PublicKey.X) != 0 || publicKey.Y.Cmp(key.PublicKey.Y) != 0 {
		return nil, nil, errors.New("certificate and private key do not match")
	}
	return certificate, key, nil
}

func (a *Authority) generateLeaf(host string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"Remask Local Proxy"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, &key.PublicKey, a.privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certificatePEM, keyPEM)
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return strings.Trim(strings.ToLower(host), "[] .")
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
