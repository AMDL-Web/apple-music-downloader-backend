package applemusic

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// developerTokenSigner mints Apple Music developer tokens (ES256 JWTs) from a
// Media Services .p8 private key. The parsed key is held in memory so repeated
// signing does not re-read the file.
type developerTokenSigner struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string
}

func newDeveloperTokenSigner(keyPath, keyID, teamID string) (*developerTokenSigner, error) {
	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read apple music private key: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("apple music private key %q is not valid PEM", keyPath)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse apple music private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apple music private key is not an ECDSA key")
	}
	return &developerTokenSigner{key: key, keyID: keyID, teamID: teamID}, nil
}

// sign returns a signed developer token valid for 24 hours from now and the
// token's expiry time.
func (s *developerTokenSigner) sign(now time.Time) (string, time.Time, error) {
	exp := now.Add(24 * time.Hour)
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": s.keyID, "typ": "JWT"})
	if err != nil {
		return "", time.Time{}, err
	}
	payload, err := json.Marshal(map[string]any{"iss": s.teamID, "iat": now.Unix(), "exp": exp.Unix()})
	if err != nil {
		return "", time.Time{}, err
	}
	signingInput := base64URL(header) + "." + base64URL(payload)
	digest := sha256.Sum256([]byte(signingInput))
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign apple music developer token: %w", err)
	}
	// ES256 signatures are the raw r||s pair, each left-padded to 32 bytes.
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	sig.FillBytes(raw[32:])
	return signingInput + "." + base64URL(raw), exp, nil
}

func base64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
