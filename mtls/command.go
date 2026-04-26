package mtls

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"
)

var Command = &cli.Command{
	Name:        "mTLS",
	Aliases:     []string{"t"},
	Usage:       "Create certificates needed for mTLS (mutual TLS)",
	Description: "Generates a complete mTLS certificate bundle for secure client-server authentication (wss://) over untrusted networks. This command creates a self-contained PKI with: a CA certificate (ca.crt), server certificate and key (server.crt, server.key) with serverAuth EKU, and client certificate and key (client.crt, client.key) with clientAuth EKU. The CA private key is NOT persisted, ensuring no additional certificates can be issued later. To rotate certificates, regenerate the entire bundle. For trusted networks or VPNs, plain WebSocket (ws://) without TLS is acceptable.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "output",
			Aliases:  []string{"o"},
			Usage:    "Output directory for certificates (default: current directory)",
			Value:    ".",
			OnlyOnce: true,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		refTime := time.Now()

		// Generate the CA
		caDER, caPriv, err := generateCA(refTime)
		if err != nil {
			err = fmt.Errorf("failed to generate CA certificate/key pair: %w", err)
			return
		}
		// Generate the server certificate
		serverDER, serverPriv, err := generateServer(refTime, caDER, caPriv)
		if err != nil {
			err = fmt.Errorf("failed to generate server certificate/key pair: %w", err)
			return
		}
		// Generate the client certificate
		clientDER, clientPriv, err := generateClient(refTime, caDER, caPriv)
		if err != nil {
			err = fmt.Errorf("failed to generate client certificate/key pair: %w", err)
			return
		}

		// Write everything to disk
		outDir := cmd.String("output")
		if err = os.MkdirAll(outDir, 0755); err != nil {
			err = fmt.Errorf("failed to create output directory %q: %w", outDir, err)
			return
		}
		fmt.Println("mTLS certificates successfully generated:")
		var path string
		{
			// CA certificate
			if path, err = writeCertificate(outDir, "ca", caDER); err != nil {
				err = fmt.Errorf("failed to write CA certificate: %w", err)
				return
			}
			fmt.Printf("\tCA certificate: %s\n", path)
			// do NOT write CA key
		}
		{
			// Server certificate
			if path, err = writeCertificate(outDir, "server", serverDER); err != nil {
				err = fmt.Errorf("failed to write server certificate: %w", err)
				return
			}
			fmt.Printf("\tServer certificate: %s\n", path)
			// Server private key
			if path, err = writePrivateKey(outDir, "server", serverPriv); err != nil {
				err = fmt.Errorf("failed to write server private key: %w", err)
				return
			}
			fmt.Printf("\tServer private key: %s\n", path)
		}
		{
			// Client certificate
			if path, err = writeCertificate(outDir, "client", clientDER); err != nil {
				err = fmt.Errorf("failed to write client certificate: %w", err)
				return
			}
			fmt.Printf("\tClient certificate: %s\n", path)
			// Client private key
			if path, err = writePrivateKey(outDir, "client", clientPriv); err != nil {
				err = fmt.Errorf("failed to write client private key: %w", err)
				return
			}
			fmt.Printf("\tClient private key: %s\n", path)
		}
		return nil
	},
}

func writeCertificate(dir, name string, der []byte) (outputPath string, err error) {
	outputPath = filepath.Join(dir, fmt.Sprintf("%s.crt", name))
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		err = fmt.Errorf("failed to create output file at %q: %w", outputPath, err)
		return
	}
	defer output.Close()
	if err = pem.Encode(output, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	}); err != nil {
		err = fmt.Errorf("failed to encode certificate in %q: %w", outputPath, err)
		return
	}
	return
}

func writePrivateKey(dir, name string, priv crypto.PrivateKey) (outputPath string, err error) {
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		err = fmt.Errorf("failed to marshal private key: %w", err)
		return
	}
	outputPath = filepath.Join(dir, fmt.Sprintf("%s.key", name))
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		err = fmt.Errorf("failed to create output file at %q: %w", outputPath, err)
		return
	}
	defer output.Close()
	if err = pem.Encode(output, &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privBytes,
	}); err != nil {
		err = fmt.Errorf("failed to encode private key in %q: %w", outputPath, err)
		return
	}
	return
}
