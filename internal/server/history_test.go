package server

import (
	"strings"
	"testing"

	"goGioIa/internal/ollama"
	"goGioIa/internal/rag"
)

func TestTrimHistory(t *testing.T) {
	long := strings.Repeat("x", 100)
	in := []ollama.Message{
		{Role: "system", Content: "fuera"}, // roles no conversacionales se descartan
		{Role: "user", Content: "   "},     // vacíos se descartan
		{Role: "user", Content: "primera"}, // cae por la ventana
		{Role: "assistant", Content: long}, // se recorta
		{Role: "user", Content: "última"},
	}
	out := trimHistory(in, 2, 10)
	if len(out) != 2 {
		t.Fatalf("len = %d, se esperaba 2 (ventana)", len(out))
	}
	if out[0].Role != "assistant" || out[0].Content != long[:10]+"…" {
		t.Errorf("mensaje largo no recortado: %q", out[0].Content)
	}
	if out[1].Content != "última" {
		t.Errorf("orden inesperado: %q", out[1].Content)
	}
	if got := trimHistory(nil, 5, 100); len(got) != 0 {
		t.Errorf("historial vacío debe quedar vacío, hay %d", len(got))
	}
}

func TestDocContextBudget(t *testing.T) {
	out := docContext([]rag.AttachedDoc{
		{Name: "grande.txt", Text: strings.Repeat("a", maxDocContextChars+100)},
		{Name: "segundo.txt", Text: "contenido"},
	})
	if !strings.Contains(out, "… (recortado)") {
		t.Error("el documento que excede el presupuesto no se recortó")
	}
	if strings.Contains(out, "segundo.txt") {
		t.Error("con el presupuesto agotado no debería inyectarse otro documento")
	}
}
