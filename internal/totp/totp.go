package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// GenerateSecret returns a base32 TOTP secret (no padding).
func GenerateSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "="), nil
}

func ProvisioningURI(secret, email, issuer string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return fmt.Sprintf("otpauth://totp/%s:%s?%s", url.PathEscape(issuer), url.PathEscape(email), v.Encode())
}

func Verify(secret, code string, skew int) bool {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	if n := len(secret) % 8; n != 0 {
		secret += strings.Repeat("=", 8-n)
	}
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		return false
	}
	now := time.Now().Unix() / 30
	for i := -skew; i <= skew; i++ {
		if hotp(key, uint64(now+int64(i))) == code {
			return true
		}
	}
	return false
}

func hotp(key []byte, counter uint64) string {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (int(sum[off])&0x7f)<<24 | (int(sum[off+1])&0xff)<<16 | (int(sum[off+2])&0xff)<<8 | (int(sum[off+3]) & 0xff)
	return fmt.Sprintf("%06d", code%1000000)
}
