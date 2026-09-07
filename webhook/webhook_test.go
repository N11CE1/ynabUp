package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(t *testing.T, secret string, payload []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature_ValidSignatureAccepted(t *testing.T) {
	payload := []byte(`{"data":{"attributes":{"eventType":"TRANSACTION_CREATED"}}}`)
	secret := "webhook-secret"

	if !VerifySignature(payload, sign(t, secret, payload), secret) {
		t.Fatal("expected a correctly signed payload to verify")
	}
}

func TestVerifySignature_WrongSecretRejected(t *testing.T) {
	payload := []byte(`{"data":{}}`)

	sig := sign(t, "correct-secret", payload)
	if VerifySignature(payload, sig, "wrong-secret") {
		t.Fatal("expected verification to fail with the wrong secret")
	}
}

func TestVerifySignature_TamperedBodyRejected(t *testing.T) {
	secret := "webhook-secret"
	original := []byte(`{"data":{"amount":100}}`)
	tampered := []byte(`{"data":{"amount":999}}`)

	sig := sign(t, secret, original)
	if VerifySignature(tampered, sig, secret) {
		t.Fatal("expected verification to fail when the body doesn't match what was signed")
	}
}

func TestVerifySignature_MalformedHexRejected(t *testing.T) {
	payload := []byte(`{"data":{}}`)

	if VerifySignature(payload, "not-valid-hex!!", "any-secret") {
		t.Fatal("expected a non-hex signature header to fail rather than panic or false-accept")
	}
}

func TestVerifySignature_EmptySignatureRejected(t *testing.T) {
	payload := []byte(`{"data":{}}`)

	if VerifySignature(payload, "", "any-secret") {
		t.Fatal("expected an empty signature header to be rejected")
	}
}
