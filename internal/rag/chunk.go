package rag

import (
	"strings"

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
			out = append(out, chunk{Index: idx, Page: p.Number, Text: part})
			idx++
		}
	}
	return out
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

// capChunks garantiza que ningún chunk supere maxChars: los que exceden se
// re-trocean (sin solape) y la lista completa se reindexa. Es determinista:
// mismos datos y configuración → mismos índices, condición necesaria para
// reanudar una ingesta sin duplicar vectores.
func capChunks(chunks []chunk, maxChars int) []chunk {
	if maxChars <= 0 {
		return chunks
	}
	out := make([]chunk, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Text) <= maxChars {
			out = append(out, c)
			continue
		}
		for _, part := range splitText(c.Text, maxChars, 0) {
			out = append(out, chunk{Page: c.Page, Text: part})
		}
	}
	for i := range out {
		out[i].Index = i
	}
	return out
}

// estimateTokens aproxima el nº de tokens (≈ 4 caracteres por token).
func estimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 {
		n = 1
	}
	return n
}
