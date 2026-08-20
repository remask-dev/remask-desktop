// remask-license is an internal signing utility. It must run only in a trusted
// environment; generated private keys must never be bundled with Remask.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/remask-dev/remask-core/internal/license"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "issue":
		err = issue(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: remask-license <keygen|issue> [options]")
	os.Exit(2)
}

func keygen(arguments []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	output := flags.String("private-key", "", "private key PEM output path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" {
		return fmt.Errorf("--private-key is required")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	fmt.Printf("key_id=%s\npublic_key=%s\n", license.DefaultKeyID, base64.StdEncoding.EncodeToString(publicKey))
	return nil
}

func issue(arguments []string) error {
	flags := flag.NewFlagSet("issue", flag.ContinueOnError)
	privateKeyPath := flags.String("private-key", "", "private key PEM path")
	keyID := flags.String("key-id", license.DefaultKeyID, "signing key identifier")
	deviceID := flags.String("device-id", "", "Remask device ID")
	edition := flags.String("edition", "professional", "license edition")
	email := flags.String("email", "", "licensed customer email (optional)")
	validFor := flags.Duration("valid-for", 365*24*time.Hour, "license validity duration")
	features := flags.String("features", "pii-protection,api-gateway,forward-proxy", "comma-separated feature names")
	orderRef := flags.String("order-ref", "", "order reference")
	output := flags.String("output", "", "license output path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *privateKeyPath == "" || *deviceID == "" || *output == "" || *validFor <= 0 {
		return fmt.Errorf("--private-key, --device-id, --output, and a positive --valid-for are required")
	}
	privateKey, err := readPrivateKey(*privateKeyPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	licenseID, err := randomLicenseID()
	if err != nil {
		return err
	}
	claims := license.Claims{
		LicenseID: licenseID, Product: license.Product, Edition: strings.TrimSpace(*edition),
		Email:    strings.TrimSpace(*email),
		DeviceID: strings.TrimSpace(*deviceID), IssuedAt: now, NotBefore: now,
		ExpiresAt: now.Add(*validFor), Features: splitFeatures(*features), OrderRef: strings.TrimSpace(*orderRef),
	}
	data, err := license.Sign(privateKey, strings.TrimSpace(*keyID), claims)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write license: %w", err)
	}
	fmt.Printf("license_id=%s\nexpires_at=%s\n", claims.LicenseID, claims.ExpiresAt.Format(time.RFC3339))
	return nil
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("private key is not PEM encoded")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not Ed25519")
	}
	return privateKey, nil
}

func randomLicenseID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "lic_" + hex.EncodeToString(value), nil
}

func splitFeatures(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
