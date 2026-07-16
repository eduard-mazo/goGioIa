package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Las versiones deben ser consecutivas desde 1: migrate() confía en el orden
// del slice y en MAX(version) para saber qué falta por aplicar.
func TestMigrationsAreOrdered(t *testing.T) {
	for i, m := range migrations {
		if m.version != i+1 {
			t.Errorf("migración en posición %d tiene versión %d, se esperaba %d", i, m.version, i+1)
		}
		if strings.TrimSpace(m.description) == "" {
			t.Errorf("migración %d sin descripción", m.version)
		}
		if len(m.statements) == 0 {
			t.Errorf("migración %d sin sentencias", m.version)
		}
		for j, stmt := range m.statements {
			if strings.TrimSpace(stmt) == "" {
				t.Errorf("migración %d: sentencia %d vacía", m.version, j)
			}
		}
	}
}

func TestIsTolerable(t *testing.T) {
	for _, code := range tolerableORA {
		err := fmt.Errorf("exec: %w", errors.New(code+": mensaje de oracle"))
		if !isTolerable(err) {
			t.Errorf("isTolerable(%s) = false, se esperaba true", code)
		}
	}
	if isTolerable(errors.New("ORA-01400: cannot insert NULL")) {
		t.Error("isTolerable aceptó un error que no es de «ya existe»")
	}
	if isTolerable(nil) {
		t.Error("isTolerable(nil) debe ser false")
	}
}

func TestBindList(t *testing.T) {
	if got := bindList(2, 3); got != ":2, :3, :4" {
		t.Errorf("bindList(2,3) = %q", got)
	}
	if got := bindList(1, 1); got != ":1" {
		t.Errorf("bindList(1,1) = %q", got)
	}
}

func TestParseVecLiteral(t *testing.T) {
	vec, err := parseVecLiteral("[1.5, -2, 3.25E-1]")
	if err != nil || len(vec) != 3 || vec[0] != 1.5 || vec[1] != -2 || vec[2] != 0.325 {
		t.Fatalf("parseVecLiteral = %v, %v", vec, err)
	}
	if _, err := parseVecLiteral("[]"); err == nil {
		t.Error("un vector vacío debe fallar")
	}
	if _, err := parseVecLiteral("[1,a]"); err == nil {
		t.Error("un literal corrupto debe fallar")
	}
}
