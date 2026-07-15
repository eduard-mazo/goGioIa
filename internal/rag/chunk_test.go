package rag

import (
	"strings"
	"testing"

	"goGioIa/internal/pdf"
)

func TestSplitTextShort(t *testing.T) {
	parts := splitText("hola mundo", 1800, 250)
	if len(parts) != 1 || parts[0] != "hola mundo" {
		t.Fatalf("esperaba un único chunk intacto, obtuve %v", parts)
	}
}

func TestSplitTextAdvancesAndCovers(t *testing.T) {
	// Párrafos artificiales para forzar cortes en límites naturales.
	text := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 200)
	parts := splitText(text, 500, 100)
	if len(parts) < 2 {
		t.Fatalf("esperaba varios chunks, obtuve %d", len(parts))
	}
	for i, p := range parts {
		if len(p) == 0 {
			t.Fatalf("chunk %d vacío", i)
		}
		if len(p) > 500 {
			t.Fatalf("chunk %d supera el tamaño máximo: %d", i, len(p))
		}
	}
	// El final del texto debe estar cubierto.
	last := parts[len(parts)-1]
	if !strings.HasSuffix(strings.TrimSpace(text), last) {
		t.Fatalf("el último chunk no cubre el final del documento")
	}
}

func TestSplitTextNoOverlapLargerThanSize(t *testing.T) {
	text := strings.Repeat("abcdefghij ", 300)
	parts := splitText(text, 100, 100) // solape inválido → se normaliza
	if len(parts) == 0 {
		t.Fatal("sin chunks")
	}
}

func TestChunkPagesKeepsPageNumbers(t *testing.T) {
	pages := []pdf.Page{
		{Number: 1, Text: "texto de la primera página"},
		{Number: 2, Text: ""},
		{Number: 3, Text: "texto de la tercera página"},
	}
	chunks := chunkPages(pages, 1800, 250)
	if len(chunks) != 2 {
		t.Fatalf("esperaba 2 chunks, obtuve %d", len(chunks))
	}
	if chunks[0].Page != 1 || chunks[1].Page != 3 {
		t.Fatalf("páginas incorrectas: %+v", chunks)
	}
	if chunks[0].Index != 0 || chunks[1].Index != 1 {
		t.Fatalf("índices incorrectos: %+v", chunks)
	}
}
