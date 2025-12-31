package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

func verifyTUIToken(token, secret string) bool {
	if token == "" || secret == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return false
	}
	if header.Alg != "HS256" {
		return false
	}

	signingInput := []byte(parts[0] + "." + parts[1])
	expected := hmac.New(sha256.New, []byte(secret))
	expected.Write(signingInput)
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	if !hmac.Equal(signature, expected.Sum(nil)) {
		return false
	}

	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return false
	}
	if expRaw, ok := payload["exp"]; ok {
		if exp, ok := expRaw.(float64); ok && int64(exp) < time.Now().Unix() {
			return false
		}
	}
	if !audAllows(payload["aud"]) {
		return false
	}
	if iss, ok := payload["iss"].(string); ok && iss != "reproq-django" {
		return false
	}
	superuser, ok := payload["superuser"].(bool)
	if !ok || !superuser {
		return false
	}
	return true
}

func audAllows(val interface{}) bool {
	switch v := val.(type) {
	case string:
		return v == "reproq-tui" || v == "reproq-worker"
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				if s == "reproq-tui" || s == "reproq-worker" {
					return true
				}
			}
		}
	}
	return false
}
