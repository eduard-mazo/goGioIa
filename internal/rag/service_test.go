package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"goGioIa/internal/ollama"
)

// sizeLimitedEmbedder simula Ollama: 400 «por tamaño» si la entrada supera
// maxChars; si no, un vector fijo. Cuenta las llamadas.
func sizeLimitedEmbedder(maxChars int, calls *int) embedFn {
	return func(_ context.Context, inputs []string) ([][]float32, error) {
		*calls++
		for _, in := range inputs {
			if len(in) > maxChars {
				return nil, &ollama.HTTPError{StatusCode: 400, Body: "input length exceeds maximum context length"}
			}
		}
		out := make([][]float32, len(inputs))
		for i := range out {
			out[i] = []float32{1, 3}
		}
		return out, nil
	}
}

func TestCapChunksSplitsOversizeAndReindexes(t *testing.T) {
	long := strings.Repeat("palabra ", 400) // 3200 caracteres
	chunks := []chunk{
		{Index: 0, Page: 1, Text: "corto"},
		{Index: 1, Page: 2, Text: strings.TrimSpace(long)},
	}
	out := capChunks(chunks, 1000)
	if len(out) < 4 {
		t.Fatalf("esperaba el chunk largo troceado, obtuve %d chunks", len(out))
	}
	for i, c := range out {
		if c.Index != i {
			t.Fatalf("índices no consecutivos: chunk %d tiene Index %d", i, c.Index)
		}
		if len(c.Text) > 1000 {
			t.Fatalf("chunk %d supera el tope: %d caracteres", i, len(c.Text))
		}
		if c.Text == "" {
			t.Fatalf("chunk %d vacío", i)
		}
	}
	if out[0].Page != 1 || out[1].Page != 2 {
		t.Fatalf("páginas perdidas al trocear: %+v", out[:2])
	}
	// Determinismo: la misma entrada produce el mismo troceo (requisito de la
	// reanudación por índice).
	again := capChunks([]chunk{{Index: 0, Page: 1, Text: "corto"}, {Index: 1, Page: 2, Text: strings.TrimSpace(long)}}, 1000)
	if len(again) != len(out) {
		t.Fatalf("el troceo no es determinista: %d vs %d", len(again), len(out))
	}
	for i := range out {
		if again[i] != out[i] {
			t.Fatalf("el troceo no es determinista en el chunk %d", i)
		}
	}
}

func TestEmbedSplittingRecoversFromOversize(t *testing.T) {
	calls := 0
	embed := sizeLimitedEmbedder(50, &calls)
	text := strings.TrimSpace(strings.Repeat("palabra ", 20)) // ~160 caracteres

	vec, err := embedSplitting(context.Background(), embed, "", text, maxSplitDepth)
	if err != nil {
		t.Fatalf("esperaba recuperación troceando: %v", err)
	}
	if len(vec) != 2 || vec[0] != 1 || vec[1] != 3 {
		t.Fatalf("el promedio de hijos idénticos debe conservar el vector: %v", vec)
	}
	if calls < 3 {
		t.Fatalf("esperaba al menos un intento padre + hijos, hubo %d llamadas", calls)
	}
}

func TestEmbedSplittingDoesNotSplitOtherErrors(t *testing.T) {
	calls := 0
	embed := func(_ context.Context, _ []string) ([][]float32, error) {
		calls++
		return nil, &ollama.HTTPError{StatusCode: 400, Body: "model not found"}
	}
	_, err := embedSplitting(context.Background(), embed, "", "cualquier texto", maxSplitDepth)
	httpErr, ok := errors.AsType[*ollama.HTTPError](err)
	if !ok || httpErr.Body != "model not found" {
		t.Fatalf("esperaba el 400 original sin trocear, obtuve %v", err)
	}
	if calls != 1 {
		t.Fatalf("un 400 ajeno al tamaño no debe reintentar ni trocear: %d llamadas", calls)
	}
}

func TestEmbedSplittingGivesUpAtMaxDepth(t *testing.T) {
	calls := 0
	embed := sizeLimitedEmbedder(0, &calls) // todo es «demasiado grande»
	if _, err := embedSplitting(context.Background(), embed, "", "aa bb", 1); err == nil {
		t.Fatal("esperaba error al agotar la profundidad de troceo")
	}
}

func TestPendingChunksSkipsAlreadyEmbedded(t *testing.T) {
	chunks := []chunk{{Index: 0}, {Index: 1}, {Index: 2}, {Index: 3}}
	pending := pendingChunks(chunks, map[int]bool{0: true, 2: true})
	if len(pending) != 2 || pending[0].Index != 1 || pending[1].Index != 3 {
		t.Fatalf("reanudación incorrecta: %+v", pending)
	}
	// Sin avance previo se procesa todo.
	if got := pendingChunks(chunks, nil); len(got) != len(chunks) {
		t.Fatalf("sin avance previo deben quedar todos: %d", len(got))
	}
}

func TestHalveTextPrefersWhitespace(t *testing.T) {
	left, right := halveText("uno dos tres cuatro")
	if left == "" || right == "" {
		t.Fatalf("mitades vacías: %q / %q", left, right)
	}
	if strings.Contains(left, "tres") {
		t.Fatalf("el corte debería caer cerca de la mitad: %q / %q", left, right)
	}
}

func TestMeanVec(t *testing.T) {
	got, err := meanVec([]float32{0, 2}, []float32{2, 4})
	if err != nil || got[0] != 1 || got[1] != 3 {
		t.Fatalf("promedio incorrecto: %v (%v)", got, err)
	}
	if _, err := meanVec([]float32{1}, []float32{1, 2}); err == nil {
		t.Fatal("esperaba error por dimensiones incompatibles")
	}
}
