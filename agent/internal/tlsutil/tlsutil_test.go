package tlsutil

import (
	"crypto/tls"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestMutualTLSHandshake(t *testing.T) {
	dir := t.TempDir()
	ca, err := GenerateCA("test-ca")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(dir, "ca", ca.CertPEM, ca.KeyPEM); err != nil {
		t.Fatal(err)
	}

	serverCert, serverKey, err := ca.Issue(IssueOptions{CommonName: "agent", DNSNames: []string{"localhost"}, ServerAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(dir, "agent", serverCert, serverKey); err != nil {
		t.Fatal(err)
	}

	clientCert, clientKey, err := ca.Issue(IssueOptions{CommonName: "controller", ClientAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(dir, "controller", clientCert, clientKey); err != nil {
		t.Fatal(err)
	}

	serverTLS, err := ServerConfig(filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key"), filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := ClientConfig(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "controller.crt"), filepath.Join(dir, "controller.key"))
	if err != nil {
		t.Fatal(err)
	}
	clientTLS.ServerName = "localhost"

	if err := handshake(serverTLS, clientTLS); err != nil {
		t.Fatalf("mTLS handshake failed: %v", err)
	}
}

func TestMutualTLSRejectsMissingClientCertificate(t *testing.T) {
	dir := t.TempDir()
	ca, err := GenerateCA("test-ca")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(dir, "ca", ca.CertPEM, ca.KeyPEM); err != nil {
		t.Fatal(err)
	}
	serverCert, serverKey, err := ca.Issue(IssueOptions{CommonName: "agent", DNSNames: []string{"localhost"}, ServerAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(dir, "agent", serverCert, serverKey); err != nil {
		t.Fatal(err)
	}

	serverTLS, err := ServerConfig(filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key"), filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := ClientConfig(filepath.Join(dir, "ca.crt"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	clientTLS.ServerName = "localhost"

	if err := handshake(serverTLS, clientTLS); err == nil {
		t.Fatal("expected handshake failure without client certificate")
	}
}

func handshake(serverTLS, clientTLS *tls.Config) error {
	serverConn, clientConn := net.Pipe()
	deadline := time.Now().Add(3 * time.Second)
	_ = serverConn.SetDeadline(deadline)
	_ = clientConn.SetDeadline(deadline)

	errCh := make(chan error, 1)
	go func() {
		tlsServer := tls.Server(serverConn, serverTLS)
		errCh <- tlsServer.Handshake()
		_ = tlsServer.Close()
	}()

	tlsClient := tls.Client(clientConn, clientTLS)
	err := tlsClient.Handshake()
	_ = tlsClient.Close()

	if serverErr := <-errCh; err == nil {
		err = serverErr
	}

	return err
}
