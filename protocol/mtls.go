package protocol

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
)

const (
	TLSVersion = tls.VersionTLS13
)

func GetServerTLSConfig(caPath, certPath, keyPath string) (tlsConf *tls.Config, err error) {
	// Load server certificate
	serverCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		err = fmt.Errorf("failed to load server cert and key: %w", err)
		return
	}
	// Load CA certificate and init CA pool
	caPEM, err := readFile(caPath)
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
		MinVersion:   TLSVersion,
	}
	return
}

func readFile(path string) (content []byte, err error) {
	fd, err := os.Open(path)
	if err != nil {
		err = fmt.Errorf("failed to open file: %w", err)
		return
	}
	defer fd.Close()
	if content, err = io.ReadAll(fd); err != nil {
		err = fmt.Errorf("failed to read file: %w", err)
		return
	}
	return
}
