package i2pcontrol

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// selfSignedCertPEM builds a throwaway self-signed cert+key for tests.
func selfSignedCertPEM(t *testing.T) (certPEM []byte, tlsCert tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "i2pd.i2pcontrol"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	tlsCert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, tlsCert
}

func startTLSRouter(t *testing.T, tlsCert tls.Certificate) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		if strings.Contains(string(b[:n]), "\"Authenticate\"") {
			w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"API":1,"Token":"tok"}}`))
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":"2","result":{"i2p.router.version":"2.56.0"}}`))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestPinningAcceptsMatchingCert(t *testing.T) {
	certPEM, tlsCert := selfSignedCertPEM(t)
	srv := startTLSRouter(t, tlsCert)

	c, err := New("pinned", srv.URL, "pw", WithPinnedCertPEM(certPEM))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("pinned poll failed: %v", err)
	}
	if s.Version != "2.56.0" {
		t.Errorf("version = %q", s.Version)
	}
}

func TestPinningRejectsDifferentCert(t *testing.T) {
	otherPEM, _ := selfSignedCertPEM(t) // pinned: a cert the server does NOT present
	_, serverCert := selfSignedCertPEM(t)
	srv := startTLSRouter(t, serverCert)

	c, err := New("evil", srv.URL, "pw", WithPinnedCertPEM(otherPEM))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Poll(context.Background())
	if err == nil {
		t.Fatal("poll must fail when server cert differs from the pinned one")
	}
	if !strings.Contains(err.Error(), "does not match the pinned one") {
		t.Errorf("unexpected error: %v", err)
	}
	// Error must not contain the password.
	if strings.Contains(err.Error(), "pw") && !strings.Contains(err.Error(), "router evil") {
		t.Errorf("error may leak password: %v", err)
	}
}

func TestNoPinningUsesNormalVerification(t *testing.T) {
	// Default (no pin, no insecure) against a self-signed server must fail
	// chain verification — verification is never silently disabled.
	_, serverCert := selfSignedCertPEM(t)
	srv := startTLSRouter(t, serverCert)
	c, err := New("plain", srv.URL, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Poll(context.Background()); err == nil {
		t.Fatal("expected TLS verification failure without pinning")
	}
}

func TestInsecureTLSIsExplicit(t *testing.T) {
	_, serverCert := selfSignedCertPEM(t)
	srv := startTLSRouter(t, serverCert)
	c, err := New("insec", srv.URL, "pw", WithInsecureTLS())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("insecure (opt-in) poll should work: %v", err)
	}
}
