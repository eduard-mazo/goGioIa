package rag

import (
	"strings"
	"testing"

	"goGioIa/internal/store"
)

const testTemplate = "### Contexto\n{context}\n\n### Pregunta\n{question}"

func TestRenderPromptWithAttachments(t *testing.T) {
	sources := []store.SearchResult{{FileName: "manual.pdf", PageNumber: 3, Text: "texto del chunk"}}
	attached := []AttachedDoc{{Name: "servidor.log", Text: "ERROR conexión perdida"}}

	out := renderPrompt(testTemplate, "¿qué pasó?", sources, attached)

	for _, want := range []string{
		"[Fuente 1] manual.pdf (pág. 3)",
		"[Adjunto 1] servidor.log",
		"ERROR conexión perdida",
		"### Pregunta\n¿qué pasó?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("el prompt no contiene %q:\n%s", want, out)
		}
	}
}

func TestRenderPromptAttachmentBudget(t *testing.T) {
	huge := strings.Repeat("a", maxAttachChars+500)
	attached := []AttachedDoc{{Name: "grande.txt", Text: huge}, {Name: "segundo.txt", Text: "bbb"}}

	out := renderPrompt(testTemplate, "p", nil, attached)

	if !strings.Contains(out, "… (recortado)") {
		t.Error("el adjunto que excede el presupuesto no se recortó")
	}
	if strings.Contains(out, "[Adjunto 2]") {
		t.Error("con el presupuesto agotado no debería inyectarse otro adjunto")
	}
	if !strings.Contains(out, "(La base de conocimiento no devolvió resultados") {
		t.Error("sin fuentes debe indicarse que la base no devolvió resultados")
	}
}

func TestRenderPromptNoPageForTextSources(t *testing.T) {
	sources := []store.SearchResult{{FileName: "app.log", PageNumber: 0, Text: "traza"}}
	out := renderPrompt(testTemplate, "p", sources, nil)
	if !strings.Contains(out, "[Fuente 1] app.log\n") || strings.Contains(out, "pág. 0") {
		t.Errorf("las fuentes sin página no deben citar «pág.»:\n%s", out)
	}
}

func TestQuestionHashNormalizes(t *testing.T) {
	a := questionHash("  ¿Qué es   EPM? ")
	b := questionHash("¿qué es epm?")
	if a != b {
		t.Error("el hash debe ignorar mayúsculas y espacios repetidos")
	}
	if a == questionHash("otra pregunta") {
		t.Error("preguntas distintas no deben colisionar")
	}
}

func TestPreview(t *testing.T) {
	got := preview("  hola \n\n  mundo   con\tespacios  ", 80)
	if got != "hola mundo con espacios" {
		t.Fatalf("preview normaliza mal: %q", got)
	}
	got = preview("áéíóú áéíóú", 7)
	if got != "áéíóú á…" {
		t.Fatalf("preview recorta mal (runas): %q", got)
	}
}

func TestSanitizeTranslation(t *testing.T) {
	if got := sanitizeTranslation("  \"Restart the JBoss service\"  ", "reinicia el servicio JBoss"); got != "Restart the JBoss service" {
		t.Fatalf("no limpió comillas: %q", got)
	}
	if got := sanitizeTranslation("   ", "pregunta original"); got != "pregunta original" {
		t.Fatalf("vacío debe volver al original: %q", got)
	}
	long := strings.Repeat("bla ", 500)
	if got := sanitizeTranslation(long, "corta"); got != "corta" {
		t.Fatalf("respuesta desproporcionada debe volver al original")
	}
}
