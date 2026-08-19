package webui

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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateSelfSignedCert writes a throwaway cert/key pair for localhost to
// dir and returns their paths, so tests don't depend on mkcert being
// installed.
func generateSelfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certOut, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		t.Fatalf("encode cert: %v", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyOut, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatalf("encode key: %v", err)
	}
	return certFile, keyFile
}

// reserveFreeAddress hands back a loopback address likely to be free: it
// opens a listener, reads the OS-assigned port, and closes it immediately
// so Serve can bind the same address.
func reserveFreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func waitForServer(t *testing.T, dial func() error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := dial(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not become reachable: %v", lastErr)
}

func TestServerServeTLS(t *testing.T) {
	certFile, keyFile := generateSelfSignedCert(t, t.TempDir())
	address := reserveFreeAddress(t)

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx, address, certFile, keyFile, "") }()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	waitForServer(t, func() error {
		response, err := client.Get("https://" + address + "/api/status")
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	})

	response, err := client.Get("https://" + address + "/api/status")
	if err != nil {
		t.Fatalf("https request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", response.StatusCode)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

func TestServerServePlainHTTPWhenNoCert(t *testing.T) {
	address := reserveFreeAddress(t)

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx, address, "", "", "") }()

	waitForServer(t, func() error {
		response, err := http.Get("http://" + address + "/api/status")
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	})

	response, err := http.Get("http://" + address + "/api/status")
	if err != nil {
		t.Fatalf("http request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", response.StatusCode)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

// TestServerServeTLSWithHTTPFallback covers the corporate-MDM-phone case
// (docs/tls.md): TLS on the primary address, plain HTTP on a second
// address for a device that can never be made to trust the cert.
func TestServerServeTLSWithHTTPFallback(t *testing.T) {
	certFile, keyFile := generateSelfSignedCert(t, t.TempDir())
	tlsAddress := reserveFreeAddress(t)
	httpAddress := reserveFreeAddress(t)

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx, tlsAddress, certFile, keyFile, httpAddress) }()

	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	waitForServer(t, func() error {
		response, err := tlsClient.Get("https://" + tlsAddress + "/api/status")
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	})
	waitForServer(t, func() error {
		response, err := http.Get("http://" + httpAddress + "/api/status")
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	})

	tlsResponse, err := tlsClient.Get("https://" + tlsAddress + "/api/status")
	if err != nil {
		t.Fatalf("https request failed: %v", err)
	}
	defer tlsResponse.Body.Close()
	if tlsResponse.StatusCode != http.StatusOK {
		t.Errorf("https status = %d, want 200", tlsResponse.StatusCode)
	}

	httpResponse, err := http.Get("http://" + httpAddress + "/api/status")
	if err != nil {
		t.Fatalf("http fallback request failed: %v", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		t.Errorf("http fallback status = %d, want 200", httpResponse.StatusCode)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

// TestServerServeIgnoresHTTPFallbackWithoutTLS confirms the fallback
// address is only meaningful when TLS is actually enabled — without a
// cert, the primary listener is already plain HTTP.
func TestServerServeIgnoresHTTPFallbackWithoutTLS(t *testing.T) {
	address := reserveFreeAddress(t)
	unusedFallback := reserveFreeAddress(t)

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx, address, "", "", unusedFallback) }()

	waitForServer(t, func() error {
		response, err := http.Get("http://" + address + "/api/status")
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	})

	if _, err := http.Get("http://" + unusedFallback + "/api/status"); err == nil {
		t.Error("expected the fallback address to remain unused when TLS is disabled")
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}
