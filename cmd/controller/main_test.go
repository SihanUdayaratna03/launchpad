package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	const secret = "test-secret"
	body := []byte(`{"action":"opened","number":12}`)
	c := &controller{secret: []byte(secret)}

	tests := []struct {
		name   string
		header string
		body   []byte
		want   bool
	}{
		{"valid signature", sign(secret, body), body, true},
		{"signed with wrong secret", sign("other-secret", body), body, false},
		{"body changed after signing", sign(secret, body), []byte(`{"action":"closed","number":12}`), false},
		{"empty header", "", body, false},
		{"prefix only", "sha256=", body, false},
		{"missing prefix", sign(secret, body)[len("sha256="):], body, false},
		{"wrong algorithm prefix", "sha1=" + hex.EncodeToString([]byte("abc")), body, false},
		{"not hex", "sha256=zzzz", body, false},
		{"truncated signature", sign(secret, body)[:20], body, false},
		{"empty body signed correctly", sign(secret, nil), nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.verifySignature(tt.header, tt.body); got != tt.want {
				t.Errorf("verifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}
