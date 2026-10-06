package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"os"
)

// getSecret reads the shared secret the tokens are signed with from the
// environment. The fallback only exists so a development setup starts without
// extra configuration; a deployment has to set SHARE_SECRET.
func getSecret() []byte {
	s := os.Getenv("SHARE_SECRET")
	if s == "" {
		s = "smart-library-dev-secret"
	}

	return []byte(s)
}

// verifyHMAC reports whether signature is the HMAC-SHA256 of signingInput.
// The comparison is the constant time one from crypto/hmac: a byte by byte
// comparison would leak the position of the first wrong character.
func verifyHMAC(signingInput, signature string, secret []byte) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

// base64URLDecode decodes a base64url string without padding, the encoding the
// segments of a JWT use.
func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
