package security

import (
	"testing"
	"time"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("password should verify")
	}
	if VerifyPassword("wrong", hash) {
		t.Fatal("wrong password should not verify")
	}
	for _, malformed := range []string{
		"argon2id$v=18$m=65536,t=1,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"argon2id$v=19$m=1048576,t=1,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"argon2id$v=19$m=65536,t=1,p=4,x=1$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if VerifyPassword("correct horse battery staple", malformed) {
			t.Fatalf("malformed password hash should be rejected: %s", malformed)
		}
	}
}

func TestSessionToken(t *testing.T) {
	token, err := SignSession("secret", TokenClaims{Subject: 7, Role: "admin", Expiry: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifySession("secret", token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != 7 || claims.Role != "admin" {
		t.Fatalf("bad claims: %#v", claims)
	}
	if _, err := VerifySession("other", token); err == nil {
		t.Fatal("wrong secret should fail")
	}
}

func TestValidateControllerURLProductionRejectsPublicHTTP(t *testing.T) {
	if _, err := ValidateControllerURL("http://203.0.113.10:8080", false, false); err == nil {
		t.Fatal("production public http should be rejected")
	}
	if _, err := ValidateControllerURL("http://127.0.0.1:8080", false, false); err != nil {
		t.Fatalf("localhost http should be allowed: %v", err)
	}
}

func TestTaskEnvelopeSignatureCoversEnvelope(t *testing.T) {
	task := TaskEnvelope{ID: 1, ServerID: 2, Type: "apply_core_config", ConfigVersion: 3, Nonce: "n", PayloadJSON: `{"x":1}`}
	sig := SignTaskEnvelope("secret", task)
	task.ConfigVersion = 4
	if next := SignTaskEnvelope("secret", task); next == sig {
		t.Fatal("v2 signature must cover config_version")
	}
}

func TestEncryptSecretRoundTripAndPurposeBinding(t *testing.T) {
	encrypted, err := EncryptSecret("persistent-master", "certificate-private-key", "private material")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "private material" {
		t.Fatal("secret was not encrypted")
	}
	plaintext, err := DecryptSecret("persistent-master", "certificate-private-key", encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "private material" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	if _, err := DecryptSecret("persistent-master", "dns-credential", encrypted); err == nil {
		t.Fatal("encrypted secret should be bound to its purpose")
	}
}
