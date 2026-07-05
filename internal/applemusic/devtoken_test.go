package applemusic

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeP8Key(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path, key
}

func TestDeveloperTokenSignerSignsValidES256(t *testing.T) {
	path, key := writeP8Key(t)
	signer, err := newDeveloperTokenSigner(path, "KID1234567", "TEAM123456")
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	token, exp, err := signer.sign(now, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if want := now.Add(24 * time.Hour); !exp.Equal(want) {
		t.Fatalf("exp = %v, want %v", exp, want)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}

	var header map[string]string
	decodeSegment(t, parts[0], &header)
	if header["alg"] != "ES256" || header["kid"] != "KID1234567" || header["typ"] != "JWT" {
		t.Fatalf("unexpected header %#v", header)
	}

	var payload map[string]any
	decodeSegment(t, parts[1], &payload)
	if payload["iss"] != "TEAM123456" {
		t.Fatalf("iss = %v", payload["iss"])
	}
	if int64(payload["iat"].(float64)) != now.Unix() {
		t.Fatalf("iat = %v, want %d", payload["iat"], now.Unix())
	}
	if int64(payload["exp"].(float64)) != now.Add(24*time.Hour).Unix() {
		t.Fatalf("exp claim = %v", payload["exp"])
	}
	if _, ok := payload["origin"]; ok {
		t.Fatalf("origin claim should be absent when no origins are given, payload %#v", payload)
	}

	// Verify the ES256 signature against the public key.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature decode: len=%d err=%v", len(sig), err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("signature verification failed")
	}
}

func TestDeveloperTokenSignerCustomTTLAndOrigins(t *testing.T) {
	path, _ := writeP8Key(t)
	signer, err := newDeveloperTokenSigner(path, "KID1234567", "TEAM123456")
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	origins := []string{"https://player.example.com", "https://beta.example.com"}
	token, exp, err := signer.sign(now, time.Hour, origins)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if want := now.Add(time.Hour); !exp.Equal(want) {
		t.Fatalf("exp = %v, want %v", exp, want)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	var payload map[string]any
	decodeSegment(t, parts[1], &payload)
	if int64(payload["exp"].(float64)) != now.Add(time.Hour).Unix() {
		t.Fatalf("exp claim = %v", payload["exp"])
	}
	claim, ok := payload["origin"].([]any)
	if !ok {
		t.Fatalf("origin claim missing or not a list: %#v", payload["origin"])
	}
	if len(claim) != len(origins) {
		t.Fatalf("origin claim has %d entries, want %d", len(claim), len(origins))
	}
	for i, origin := range origins {
		if claim[i] != origin {
			t.Fatalf("origin[%d] = %v, want %s", i, claim[i], origin)
		}
	}
}

func TestNewDeveloperTokenSignerRejectsBadKeys(t *testing.T) {
	if _, err := newDeveloperTokenSigner(filepath.Join(t.TempDir(), "missing.p8"), "k", "t"); err == nil {
		t.Fatal("expected error for missing file")
	}

	notPEM := filepath.Join(t.TempDir(), "bad.p8")
	if err := os.WriteFile(notPEM, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeveloperTokenSigner(notPEM, "k", "t"); err == nil {
		t.Fatal("expected error for non-PEM file")
	}

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	rsaPath := filepath.Join(t.TempDir(), "rsa.p8")
	if err := os.WriteFile(rsaPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeveloperTokenSigner(rsaPath, "k", "t"); err == nil {
		t.Fatal("expected error for non-ECDSA key")
	}
}

func decodeSegment(t *testing.T, seg string, out any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal segment: %v", err)
	}
}
