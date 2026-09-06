package pdf

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeCollapsesDotLeaders(t *testing.T) {
	in := "5.2.5 Assigned Rules ................................ 32\n" +
		"Message Header . . . . . . . . . . . . . . 16"
	got := normalize(in)
	want := "5.2.5 Assigned Rules 32\nMessage Header 16"
	if got != want {
		t.Fatalf("normalize:\n got %q\nwant %q", got, want)
	}
}

func TestNormalizeKeepsVersionsAndEllipsis(t *testing.T) {
	in := "La versión 2.2.0.5 sigue... intacta."
	if got := normalize(in); got != in {
		t.Fatalf("normalize alteró texto legítimo: %q", got)
	}
}

func TestStripBoilerplateRemovesRepeatedHeaders(t *testing.T) {
	header := "Spectrum Power 7, HW and Architecture, Functional Specification"
	temas := []string{"alarmas", "topología", "históricos", "interfaces", "redundancia", "usuarios"}
	pages := make([]Page, 6)
	for i := range pages {
		pages[i] = Page{Number: i + 1, Text: header + "\n" +
			fmt.Sprintf("%d of 92 Spectrum Power 7 Functional Specification\n", i+10) +
			"Contenido sobre " + temas[i] + " que solo aparece en esta página."}
	}
	out := stripBoilerplate(pages)
	for i, p := range out {
		if strings.Contains(p.Text, "Spectrum Power 7") {
			t.Fatalf("encabezado/pie no eliminado en pág %d: %q", p.Number, p.Text)
		}
		if !strings.Contains(p.Text, temas[i]) {
			t.Fatalf("contenido legítimo eliminado en pág %d: %q", p.Number, p.Text)
		}
	}
}

func TestStripBoilerplateLeavesShortDocsAlone(t *testing.T) {
	pages := []Page{
		{Number: 1, Text: "Encabezado\ncontenido uno"},
		{Number: 2, Text: "Encabezado\ncontenido dos"},
	}
	out := stripBoilerplate(pages)
	if !strings.Contains(out[0].Text, "Encabezado") {
		t.Fatal("un documento de 2 páginas no debe tocarse")
	}
}
