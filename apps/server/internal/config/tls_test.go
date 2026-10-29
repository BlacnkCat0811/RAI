package config

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTLSFailsClosedAndProbeValidatesChainHostname(t *testing.T) {
	for _, key := range []string{"RAI_TLS_CERT_FILE", "RAI_TLS_KEY_FILE", "RAI_REQUIRE_TLS", "RAI_TLS_CA_FILE", "RAI_TLS_SERVER_NAME"} {
		t.Setenv(key, "")
	}
	if config, err := TLS(); err != nil || config != nil {
		t.Fatal("development TLS", err)
	}
	t.Setenv("RAI_REQUIRE_TLS", "1")
	if _, err := TLS(); err == nil {
		t.Fatal("required TLS accepted missing pair")
	}
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(503)
		}
	}))
	defer s.Close()
	t.Setenv("RAI_LISTEN_ADDR", s.Listener.Addr().String())
	t.Setenv("RAI_TLS_SERVER_NAME", "example.com") // Go httptest's generated SAN.
	if err := Probe(false); err == nil {
		t.Fatal("untrusted chain accepted")
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAI_TLS_CA_FILE", file)
	if err := Probe(false); err != nil {
		t.Fatal("trusted liveness", err)
	}
	if err := Probe(true); err == nil {
		t.Fatal("503 readiness accepted")
	}
	t.Setenv("RAI_TLS_SERVER_NAME", "wrong.example")
	if err := Probe(false); err == nil {
		t.Fatal("hostname mismatch accepted")
	}
}
