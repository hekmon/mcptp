package mtls

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"
)

const (
	mTLSValidity         = 10 * 365 * 24 * time.Hour
	mTLSOrg              = "mcptp"
	mTLSCACommonName     = "mcptp-CA"
	mTLSServerCommonName = "mcptp-server"
	mTLSClientCommonName = "mcptp-client"
)

// generateCA creates a new CA private key and self-signed certificate
func generateCA(refTime time.Time) (caDER []byte, caPriv crypto.PrivateKey, err error) {
	// Generate CA key
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		err = fmt.Errorf("failed to generate an Ed25519 key: %w", err)
		return
	}
	// Create CA self-signed Certificat
	caTmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			Organization: []string{mTLSOrg},
			CommonName:   mTLSCACommonName,
		},
		NotBefore:             refTime,
		NotAfter:              refTime.Add(mTLSValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	if caDER, err = x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caPub, caPriv); err != nil {
		err = fmt.Errorf("failed to create CA certificate: %w", err)
		return
	}
	return
}

func generateServer(refTime time.Time, caDER []byte, caPriv crypto.PrivateKey) (serverDER []byte, serverPriv crypto.PrivateKey, err error) {
	// Parse CA certificate in a usable format
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		err = fmt.Errorf("failed to parse CA certificate: %w", err)
		return
	}
	// Generate server key
	serverPub, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		err = fmt.Errorf("failed to generate an Ed25519 key: %w", err)
		return
	}
	// Create server certificate
	serverTmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			Organization: []string{mTLSOrg},
			CommonName:   mTLSServerCommonName,
		},
		NotBefore:             refTime,
		NotAfter:              refTime.Add(mTLSValidity),
		IsCA:                  false,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if serverDER, err = x509.CreateCertificate(rand.Reader, serverTmpl, caCert, serverPub, caPriv); err != nil {
		err = fmt.Errorf("failed to create server certificate: %w", err)
		return
	}
	return
}

func generateClient(refTime time.Time, caDER []byte, caPriv crypto.PrivateKey) (clientDER []byte, clientPriv crypto.PrivateKey, err error) {
	// Parse CA certificate in a usable format
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		err = fmt.Errorf("failed to parse CA certificate: %w", err)
		return
	}
	// Generate client key
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		err = fmt.Errorf("failed to generate an Ed25519 key: %w", err)
		return
	}
	// Create client certificate
	clientTmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			Organization: []string{mTLSOrg},
			CommonName:   mTLSClientCommonName,
		},
		NotBefore:             refTime,
		NotAfter:              refTime.Add(mTLSValidity),
		IsCA:                  false,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if clientDER, err = x509.CreateCertificate(rand.Reader, clientTmpl, caCert, clientPub, caPriv); err != nil {
		err = fmt.Errorf("failed to create client certificate: %w", err)
		return
	}
	return
}

func newSerial() *big.Int {
	b := make([]byte, 16) // 128 bits
	_, _ = rand.Read(b)   // From doc: Read fills b with cryptographically secure random bytes. It never returns an error, and always fills b entirely.
	return new(big.Int).SetBytes(b)
}
