package redisx

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
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"linapi/internal/config"
)

func TestValidateSecurityRejectsRemotePlaintextRedis(t *testing.T) {
	cfg := config.RedisConfig{Addr: "10.0.0.2:6379"}
	if err := ValidateSecurity(cfg, true); err == nil {
		t.Fatal("release 模式远程明文 Redis 必须被拒绝")
	}
	cfg.TLS.Enabled = true
	if err := ValidateSecurity(cfg, true); err != nil {
		t.Fatalf("启用 TLS 后应通过: %v", err)
	}
}

func TestBuildTLSConfigDefaultsServerNameAndTLS12(t *testing.T) {
	cfg := config.RedisConfig{Addr: "redis.example.com:6380", TLS: config.RedisTLSConfig{Enabled: true}}
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg.ServerName != "redis.example.com" {
		t.Fatalf("ServerName = %q", tlsCfg.ServerName)
	}
}

func TestNewConnectsToTLSOnlyRedisWithVerifiedCA(t *testing.T) {
	serverTLS, caPEM := testRedisTLSCertificate(t)
	mr, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	caFile := filepath.Join(t.TempDir(), "redis-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := New(config.RedisConfig{
		Addr: mr.Addr(),
		TLS:  config.RedisTLSConfig{Enabled: true, ServerName: "localhost", CAFile: caFile},
	})
	if err != nil {
		t.Fatalf("受信任 CA + 正确 ServerName 的 TLS Redis 应能连接: %v", err)
	}
	defer client.Close()
}

func TestNewRejectsTLSServerNameMismatch(t *testing.T) {
	serverTLS, caPEM := testRedisTLSCertificate(t)
	mr, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	caFile := filepath.Join(t.TempDir(), "redis-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := New(config.RedisConfig{
		Addr: mr.Addr(),
		TLS:  config.RedisTLSConfig{Enabled: true, ServerName: "wrong.example", CAFile: caFile},
	}); err == nil {
		t.Fatal("TLS ServerName 不匹配必须拒绝连接")
	}
}

func testRedisTLSCertificate(t *testing.T) (*tls.Config, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, certPEM
}
