package kalshi_http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Signer implements Kalshi API request signing.
// Uses HMAC-SHA256 with the API secret to sign: timestamp + method + path.
type Signer struct {
	apiKey string
	secret string
}

func NewSigner(apiKey, secret string) *Signer {
	return &Signer{apiKey: apiKey, secret: secret}
}

func (s *Signer) Sign(req *http.Request) error {
	if s.apiKey == "" || s.secret == "" {
		return nil
	}

	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	method := req.Method
	path := req.URL.Path

	message := fmt.Sprintf("%s%s%s", ts, method, path)

	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(message))
	signature := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("KALSHI-ACCESS-KEY", s.apiKey)
	req.Header.Set("KALSHI-ACCESS-SIGNATURE", signature)
	req.Header.Set("KALSHI-ACCESS-TIMESTAMP", ts)

	return nil
}
