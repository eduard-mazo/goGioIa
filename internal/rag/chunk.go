package rag

import (
	"strings"
	"unicode"

	"goGioIa/internal/pdf"
)

// chunk es un fragmento de documento listo para vectorizar.
type chunk struct {
	Index int // posición global dentro del documento
	Page  int // página de origen (para citar la fuente)
	Text  string
}

// chunkPages trocea el texto página a página, conservando el nº de página de
// cada fragmento. size/overlap se miden en caracteres.
func chunkPages(pages []pdf.Page, size, overlap int) []chunk {
	var out []chunk
	idx := 0
	for _, p := range pages {
		text := strings.TrimSpace(p.Text)
		if text == "" {
			continue
		}
		for _, part := range splitText(text, size, overlap) {
			if !usefulChunk(part) {
				continue
			}
			out = append(out, chunk{Index: idx, Page: p.Number, Text: part})
			idx++
		}
	}
	return out
}

// usefulChunk descarta fragmentos sin señal recuperable: demasiado cortos o
// compuestos mayormente de puntuación y artefactos de extracción (glifos sin
// mapear «�», restos de índices). Vectorizarlos contamina el retrieval: son
// vecinos débiles de cualquier consulta.
func usefulChunk(s string) bool {
	const minChars = 40
	if len(s) < minChars {
		return false
	}
	letters, total := 0, 0
	for _, r := range s {
		total++
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		}
	}
	return letters*2 >= total // al menos la mitad debe ser letras o dígitos
}

// splitText divide un texto en trozos de como máximo size caracteres con
// solape, prefiriendo cortar en límites de párrafo, línea o frase para no
// partir ideas por la mitad.
func splitText(text string, size, overlap int) []string {
	if size <= 0 {
		size = 1800
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 8
	}
	if len(text) <= size {
		return []string{text}
	}

	var parts []string
	start := 0
	for start < len(text) {
		end := start + size
		if end >= len(text) {
			parts = append(parts, strings.TrimSpace(text[start:]))
			break
		}
		cut := findBreak(text, start, end)
		piece := strings.TrimSpace(text[start:cut])
		if piece != "" {
			parts = append(parts, piece)
		}
		next := cut - overlap
		if next <= start { // garantiza avance aunque el solape sea grande
			next = cut
		}
		start = next
	}
	return parts
}

// findBreak busca, dentro del último 25 % de la ventana, el mejor punto de
// corte: párrafo > salto de línea > fin de frase > espacio > corte duro.
func findBreak(text string, start, end int) int {
	window := text[start:end]
	floor := len(window) * 3 / 4
	for _, sep := range []string{"\n\n", "\n", ". ", " "} {
		if i := strings.LastIndex(window, sep); i >= floor {
			return start + i + len(sep)
		}
	}
	return end
}

// estimateTokens aproxima el nº de tokens (≈ 4 caracteres por token).
func estimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 {
		n = 1
	}
	return n
}
