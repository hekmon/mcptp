package protocol

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"os"
	"time"
)

const (
	mTLSVersion          = tls.VersionTLS13
	mTLSValidity         = 10 * 365 * 24 * time.Hour // does not expire on our own (10 years), let the user rotate if needed
	mTLSOrg              = "mcptp"
	mTLSCACommonName     = "mcptp-CA"
	mTLSServerCommonName = "mcptp-server"
	mTLSClientCommonName = "mcptp-client"
)

/*
 * Generation
 */

// GenerateCA creates a new CA private key and self-signed certificate
func GenerateCA(refTime time.Time) (caDER []byte, caPriv crypto.PrivateKey, err error) {
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

func GenerateServer(refTime time.Time, caDER []byte, caPriv crypto.PrivateKey) (serverDER []byte, serverPriv crypto.PrivateKey, err error) {
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

func GenerateClient(refTime time.Time, caDER []byte, caPriv crypto.PrivateKey) (clientDER []byte, clientPriv crypto.PrivateKey, err error) {
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

func GetServerTLSConfig(caPath, certPath, keyPath string) (tlsConf *tls.Config, err error) {
	// Load server certificate
	serverCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		err = fmt.Errorf("failed to load server cert and key: %w", err)
		return
	}
	// Load CA certificate and init CA pool
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		err = fmt.Errorf("failed to read CA cert: %w", err)
		return
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		err = fmt.Errorf("invalid %q file", caPath)
		return
	}
	// Verify server certificate
	if _, err = serverCert.Leaf.Verify(x509.VerifyOptions{
		Roots:     caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		err = fmt.Errorf("failed to verify server certificate: %w", err)
		return
	}
	// Create TLS config
	tlsConf = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   mTLSVersion,
	}
	return
}

func GetClientTLSConfig(caPath, certPath, keyPath string) (tlsConf *tls.Config, err error) {
	// Load client certificate
	clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		err = fmt.Errorf("failed to load client cert and key: %w", err)
		return
	}
	// Load CA certificate and init CA pool
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		err = fmt.Errorf("failed to read CA cert: %w", err)
		return
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		err = fmt.Errorf("invalid %q file", caPath)
		return
	}
	// Verify client certificate
	if _, err = clientCert.Leaf.Verify(x509.VerifyOptions{
		Roots:     caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		err = fmt.Errorf("failed to verify client certificate: %w", err)
		return
	}
	// Create client TLS config
	tlsConf = &tls.Config{
		Certificates:       []tls.Certificate{clientCert},
		RootCAs:            caPool,
		MinVersion:         mTLSVersion,
		InsecureSkipVerify: true, // skip hostname verification, CA trust enforced via VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) (err error) {
			_, err = cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:     caPool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return
		},
	}
	return
}
