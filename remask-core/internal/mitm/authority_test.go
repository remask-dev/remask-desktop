package mitm

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthorityPersistsRootAndSignsHostCertificate(t *testing.T) {
	directory := t.TempDir()
	first, err := NewAuthority(directory)
	if err != nil {
		t.Fatal(err)
	}
	status := first.Status()
	if !status.Ready || status.CertificatePath == "" || status.Fingerprint == "" {
		t.Fatalf("unexpected authority status: %#v", status)
	}
	if info, err := os.Stat(filepath.Join(directory, "certificates", privateKeyFilename)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key permissions: info=%v err=%v", info, err)
	}

	leaf, err := first.CertificateFor("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	verifyLeaf(t, leaf, first.RootCertificatePEM(), "api.example.com")

	second, err := NewAuthority(directory)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status().Fingerprint != status.Fingerprint {
		t.Fatalf("root certificate changed across reload: %q != %q", second.Status().Fingerprint, status.Fingerprint)
	}
}

func verifyLeaf(t *testing.T, leaf tls.Certificate, rootPEM []byte, hostname string) {
	t.Helper()
	certificate, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		t.Fatal("append root certificate")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{DNSName: hostname, Roots: roots}); err != nil {
		t.Fatal(err)
	}
}
