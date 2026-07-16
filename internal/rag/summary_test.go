package rag

import (
	"strings"
	"testing"

	"goGioIa/internal/store"
)

func TestBuildSummaryPrompt(t *testing.T) {
	long := strings.Repeat("x", maxSummaryMsgChars+50)
	out := buildSummaryPrompt("resumen previo aquí", []store.ConvMessage{
		{Role: "user", Content: "hola"},
		{Role: "assistant", Content: long},
	})
	for _, want := range []string{"### Resumen previo", "resumen previo aquí", "Usuario: hola", "Asistente: " + long[:maxSummaryMsgChars] + "…", "### Instrucción"} {
		if !strings.Contains(out, want) {
			t.Errorf("el prompt no contiene %q", want)
		}
	}
	sin := buildSummaryPrompt("", []store.ConvMessage{{Role: "user", Content: "hola"}})
	if strings.Contains(sin, "### Resumen previo") {
		t.Error("sin resumen previo no debe haber sección de resumen previo")
	}
}
