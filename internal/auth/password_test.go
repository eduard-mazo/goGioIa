package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("s3creta-Muy!Larga")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("formato PHC inesperado: %s", h)
	}
	if err := VerifyPassword("s3creta-Muy!Larga", h); err != nil {
		t.Fatalf("la contraseña correcta no verifica: %v", err)
	}
	if err := VerifyPassword("otra", h); err == nil {
		t.Fatal("una contraseña incorrecta verificó")
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("misma")
	b, _ := HashPassword("misma")
	if a == b {
		t.Fatal("dos hashes de la misma contraseña no deben coincidir (salt)")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "plaintext", "$bcrypt$x$y", "$argon2id$v=19$m=1,t=1,p=1$!!$??"} {
		if err := VerifyPassword("x", bad); err == nil {
			t.Fatalf("hash malformado aceptado: %q", bad)
		}
	}
}

func TestRandomPassword(t *testing.T) {
	p, err := RandomPassword(24)
	if err != nil || len(p) != 24 {
		t.Fatalf("RandomPassword: %q, %v", p, err)
	}
	q, _ := RandomPassword(24)
	if p == q {
		t.Fatal("dos contraseñas aleatorias idénticas")
	}
}
