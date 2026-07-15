// Package auth implementa las primitivas de autenticación de la aplicación.
// En esta fase solo el hashing de contraseñas con Argon2id (recomendación
// OWASP), codificado en formato PHC para que los parámetros viajen con el
// hash y puedan endurecerse en el futuro sin migración.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parámetros Argon2id para contraseñas de login (~64 MB de memoria por hash).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrHashFormat indica que el hash almacenado no tiene el formato PHC esperado.
var ErrHashFormat = errors.New("auth: formato de hash inválido")

// HashPassword deriva un hash Argon2id con salt aleatoria y lo codifica como
// $argon2id$v=19$m=...,t=...,p=...$<salt-b64>$<hash-b64>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generar salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword comprueba una contraseña contra un hash PHC en tiempo
// constante. Devuelve nil si coincide.
func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return ErrHashFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return ErrHashFormat
	}
	var mem, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &time, &threads); err != nil {
		return ErrHashFormat
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return ErrHashFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return ErrHashFormat
	}
	got := argon2.IDKey([]byte(password), salt, time, mem, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return errors.New("auth: contraseña incorrecta")
	}
	return nil
}

// DummyVerify consume el mismo costo que una verificación real. Se invoca en
// logins contra usuarios inexistentes para uniformar el tiempo de respuesta
// (mitiga enumeración de cuentas por timing).
func DummyVerify() {
	salt := make([]byte, argonSaltLen)
	argon2.IDKey([]byte("dummy-timing-equalizer"), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

// RandomPassword genera una contraseña aleatoria imprimible de n caracteres
// (bootstrap del admin inicial cuando no se define GOGIOIA_ADMIN_PASSWORD).
func RandomPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789!#%+"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}
