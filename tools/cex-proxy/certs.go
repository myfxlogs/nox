// certs.go 自签 CA + 服务器证书生成/复用——自 main.go 拆出（D-309 纯移动，零行为变更）。
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ensureCerts 生成自签 CA + 服务器证书到 ./certs/（已存在则复用）。
func ensureCerts(sans string) (certFile, keyFile string, err error) {
	const dir = "certs"
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	certFile, keyFile = filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if _, e1 := os.Stat(certFile); e1 == nil {
		if _, e2 := os.Stat(keyFile); e2 == nil {
			return certFile, keyFile, nil
		}
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "arb cex-proxy CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return "", "", err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: "arb cex-proxy"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	for s := range strings.SplitSeq(sans, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			leafTpl.IPAddresses = append(leafTpl.IPAddresses, ip)
		} else {
			leafTpl.DNSNames = append(leafTpl.DNSNames, s)
		}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}

	if err = writePEM(certFile, leafDER, "CERTIFICATE"); err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return "", "", err
	}
	if err = writePEM(keyFile, keyDER, "EC PRIVATE KEY"); err != nil {
		return "", "", err
	}
	if err = writePEM(filepath.Join(dir, "ca.pem"), caDER, "CERTIFICATE"); err != nil {
		return "", "", err
	}
	log.Printf("已生成证书到 %s（CA: %s）", dir, filepath.Join(dir, "ca.pem"))
	return certFile, keyFile, nil
}

func writePEM(path string, der []byte, typ string) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600)
}
