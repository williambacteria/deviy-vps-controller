package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type CertificateAuthority struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
	KeyPEM  []byte
}

type IssueOptions struct {
	CommonName string
	DNSNames   []string
	IPs        []net.IP
	ServerAuth bool
	ClientAuth bool
	Validity   time.Duration
}

func GenerateCA(commonName string) (*CertificateAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	return &CertificateAuthority{
		Cert:    cert,
		Key:     key,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  encodeKey(key),
	}, nil
}

func (ca *CertificateAuthority) Issue(options IssueOptions) ([]byte, []byte, error) {
	if options.Validity <= 0 {
		options.Validity = 365 * 24 * time.Hour
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	usage := []x509.ExtKeyUsage{}
	if options.ServerAuth {
		usage = append(usage, x509.ExtKeyUsageServerAuth)
	}
	if options.ClientAuth {
		usage = append(usage, x509.ExtKeyUsageClientAuth)
	}
	if len(usage) == 0 {
		usage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: options.CommonName},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(options.Validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usage,
		DNSNames:     options.DNSNames,
		IPAddresses:  options.IPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, nil, err
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), encodeKey(key), nil
}

func WriteBundle(dir, name string, certPEM, keyPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, name+".crt"), certPEM, 0o644); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, name+".key"), keyPEM, 0o600)
}

func ServerTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	return ServerConfig(certFile, keyFile, clientCAFile)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)

	return rand.Int(rand.Reader, limit)
}

func encodeKey(key *ecdsa.PrivateKey) []byte {
	der, _ := x509.MarshalECPrivateKey(key)

	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
