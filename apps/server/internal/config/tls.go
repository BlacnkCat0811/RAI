package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"time"
)

// TLS is optional for loopback development; the release Compose requires it.
// Certificate replacement is activated by a controlled API restart.
func TLS() (*tls.Config, error) {
	cert, key := os.Getenv("RAI_TLS_CERT_FILE"), os.Getenv("RAI_TLS_KEY_FILE")
	if cert == "" && key == "" && os.Getenv("RAI_REQUIRE_TLS") != "1" {
		return nil, nil
	}
	if cert == "" || key == "" {
		return nil, errors.New("TLS certificate and key are required")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, errors.New("TLS certificate or key unavailable")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}

// Probe always connects locally and still verifies the certificate hostname
// and chain. Private PKI may provide a CA file; verification is never disabled.
func Probe(ready bool) error {
	addr, err := ListenAddr()
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(addr)
	path := "/health"
	if ready {
		path = "/ready"
	}
	scheme := "http"
	transport := &http.Transport{Proxy: nil}
	if os.Getenv("RAI_TLS_CERT_FILE") != "" || os.Getenv("RAI_REQUIRE_TLS") == "1" {
		scheme = "https"
		name := os.Getenv("RAI_TLS_SERVER_NAME")
		if name == "" {
			return errors.New("TLS probe hostname is required")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if path := os.Getenv("RAI_TLS_CA_FILE"); path != "" {
			b, err := os.ReadFile(path)
			if err != nil || !roots.AppendCertsFromPEM(b) {
				return errors.New("TLS probe CA unavailable")
			}
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name, RootCAs: roots}
	}
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: 2 * time.Second, Transport: transport}
	resp, err := client.Get(scheme + "://127.0.0.1:" + port + path)
	if err != nil {
		return errors.New("probe connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("probe not ready")
	}
	return nil
}
