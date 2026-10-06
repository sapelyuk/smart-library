package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"os"
)

// getSecret возвращает секрет для проверки подписи JWT из переменной окружения.
func getSecret() []byte {
	s := os.Getenv("SHARE_SECRET")
	if s == "" {
		s = "smart-library-dev-secret"
	}

	return []byte(s)
}

// verifyHMAC проверяет HMAC-SHA256 подпись токена.
func verifyHMAC(signingInput, signature string, secret []byte) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

// base64URLDecode декодирует base64url-строку (без padding).
func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
