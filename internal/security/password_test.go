package security

import "testing"

func TestPasswordHashAndVerify(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if encoded == "correct horse battery staple" {
		t.Fatal("password hash contains plaintext password")
	}
	if !VerifyPassword(encoded, "correct horse battery staple") {
		t.Fatal("valid password was rejected")
	}
	if VerifyPassword(encoded, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
	if VerifyPassword("invalid", "correct horse battery staple") {
		t.Fatal("invalid encoded hash was accepted")
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("too-short"); err == nil {
		t.Fatal("short password was accepted")
	}
	if err := ValidatePassword("long-enough-password"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}
