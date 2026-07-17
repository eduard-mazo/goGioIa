package rag

import (
	"strings"
	"testing"

	"goGioIa/internal/pdf"
	"goGioIa/internal/store"
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
		{Number: 1, Text: "texto de la primera página con contenido suficiente para el filtro"},
		{Number: 2, Text: ""},
		{Number: 3, Text: "texto de la tercera página con contenido suficiente para el filtro"},
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

func TestChunkPagesSkipsJunk(t *testing.T) {
	pages := []pdf.Page{
		{Number: 1, Text: strings.Repeat("� ", 400)},                        // glifos sin mapear
		{Number: 2, Text: "ok"},                                             // demasiado corto
		{Number: 3, Text: strings.Repeat("Texto útil con contenido. ", 20)}, // válido
	}
	chunks := chunkPages(pages, 1800, 250)
	if len(chunks) != 1 {
		t.Fatalf("esperaba 1 chunk útil, hay %d", len(chunks))
	}
	if chunks[0].Page != 3 || chunks[0].Index != 0 {
		t.Fatalf("chunk inesperado: pág %d idx %d", chunks[0].Page, chunks[0].Index)
	}
}

func TestUsefulChunk(t *testing.T) {
	if usefulChunk(". . . . . . . . . . . . . . . . . . . . . . . . . . . .") {
		t.Fatal("una línea de puntos no es un chunk útil")
	}
	if !usefulChunk("El servicio JBoss se reinicia con el comando systemctl restart jboss-eap.") {
		t.Fatal("prosa normal debe ser útil")
	}
}

func TestDiversifySources(t *testing.T) {
	mk := func(doc string, sim float64) store.SearchResult {
		return store.SearchResult{DocumentID: doc, Similarity: sim}
	}
	// Un documento grande acapara los primeros puestos; otro tiene un match.
	cands := []store.SearchResult{
		mk("big", 0.80), mk("big", 0.79), mk("big", 0.78), mk("big", 0.77),
		mk("big", 0.76), mk("small", 0.75), mk("big", 0.74), mk("other", 0.73),
	}
	out := diversifySources(cands, 5, 2)
	if len(out) != 5 {
		t.Fatalf("esperaba 5, hay %d", len(out))
	}
	docs := map[string]int{}
	for _, r := range out {
		docs[r.DocumentID]++
	}
	// Cupo de 2 por doc → big:2, small:1, other:1; el 5º puesto se rellena
	// con el mejor restante (big). Lo esencial: small y other entran.
	if docs["big"] != 3 || docs["small"] != 1 || docs["other"] != 1 {
		t.Fatalf("reparto inesperado: %v", docs)
	}
	// Solo un documento en los candidatos → se rellena con él.
	cands = []store.SearchResult{mk("big", 0.8), mk("big", 0.7), mk("big", 0.6), mk("big", 0.5), mk("big", 0.4), mk("big", 0.3)}
	out = diversifySources(cands, 5, 2)
	if len(out) != 5 {
		t.Fatalf("el relleno debe completar el topK: %d", len(out))
	}
	if out[0].Similarity < out[4].Similarity {
		t.Fatal("el resultado debe quedar ordenado por afinidad")
	}
}
