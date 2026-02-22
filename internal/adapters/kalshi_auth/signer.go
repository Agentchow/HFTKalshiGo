package kalshi_auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Signer implements Kalshi API request signing using RSA-PSS with SHA-256.
// Both the HTTP and WebSocket clients share this signer.
type Signer struct {
	keyID      string
	privateKey *rsa.PrivateKey
}

// NewSignerFromFile loads an RSA private key from a PEM file and returns a
// Signer. Returns (nil, nil) when keyID or keyFilePath is empty, allowing
// callers to run without Kalshi credentials.
func NewSignerFromFile(keyID, keyFilePath string) (*Signer, error) {
	if keyID == "" || keyFilePath == "" {
		return nil, nil
	}

	pemData, err := os.ReadFile(keyFilePath)
	if err != nil {
		return nil, fmt.Errorf("read key file %s: %w", keyFilePath, err)
	}

	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", keyFilePath)
	}

	// Try PKCS#8 first, fall back to PKCS#1.
	var rsaKey *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		rsaKey, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key in %s is not RSA (got %T)", keyFilePath, parsed)
		}
	} else if pk1, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		rsaKey = pk1
	} else {
		return nil, fmt.Errorf("parse private key in %s: not PKCS#8 or PKCS#1", keyFilePath)
	}

	return &Signer{keyID: keyID, privateKey: rsaKey}, nil
}

// SignRequest sets KALSHI-ACCESS-KEY, KALSHI-ACCESS-SIGNATURE, and
// KALSHI-ACCESS-TIMESTAMP headers on req. No-op when s is nil.
func (s *Signer) SignRequest(req *http.Request) error {
	if s == nil {
		return nil
	}

	ts, sig, err := s.sign(req.Method, req.URL.Path)
	if err != nil {
		return err
	}

	req.Header.Set("KALSHI-ACCESS-KEY", s.keyID)
	req.Header.Set("KALSHI-ACCESS-SIGNATURE", sig)
	req.Header.Set("KALSHI-ACCESS-TIMESTAMP", ts)
	return nil
}

// Headers returns auth headers suitable for a WebSocket dial. The method and
// path should match the WS endpoint (e.g. "GET", "/trade-api/ws/v2").
// Returns nil when s is nil.
func (s *Signer) Headers(method, path string) http.Header {
	if s == nil {
		return nil
	}

	ts, sig, err := s.sign(method, path)
	if err != nil {
		return nil
	}

	h := http.Header{}
	h.Set("KALSHI-ACCESS-KEY", s.keyID)
	h.Set("KALSHI-ACCESS-SIGNATURE", sig)
	h.Set("KALSHI-ACCESS-TIMESTAMP", ts)
	return h
}

// Enabled reports whether this signer has credentials loaded.
func (s *Signer) Enabled() bool {
	return s != nil && s.keyID != ""
}

func (s *Signer) sign(method, path string) (timestamp, signature string, err error) {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	message := ts + method + path

	hash := sha256.Sum256([]byte(message))

	sig, err := rsa.SignPSS(rand.Reader, s.privateKey, crypto.SHA256, hash[:], &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
	})
	if err != nil {
		return "", "", fmt.Errorf("rsa sign pss: %w", err)
	}

	return ts, base64.StdEncoding.EncodeToString(sig), nil
}
