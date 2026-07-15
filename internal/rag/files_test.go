package rag

import (
	"strings"
	"testing"
)

func TestSupportedFile(t *testing.T) {
	for _, name := range []string{"doc.pdf", "Doc.PDF", "app.log", "notas.txt", "guía.md", "datos.CSV", "config.yaml", "script.sql"} {
		if !SupportedFile(name) {
			t.Errorf("SupportedFile(%q) = false, se esperaba true", name)
		}
	}
	for _, name := range []string{"foto.png", "binario.exe", "informe.docx", "sinextension", "comprimido.zip"} {
		if SupportedFile(name) {
			t.Errorf("SupportedFile(%q) = true, se esperaba false", name)
		}
	}
}

func TestDecodeTextUTF8(t *testing.T) {
	in := "línea uno\r\nlínea dos\r\r\n\n\n\nfin"
	got, err := decodeText([]byte(in))
	if err != nil {
		t.Fatalf("decodeText: %v", err)
	}
	want := "línea uno\nlínea dos\n\nfin"
	if got != want {
		t.Errorf("decodeText = %q, se esperaba %q", got, want)
	}
}

func TestDecodeTextLatin1(t *testing.T) {
	// "año" en Latin-1: 0xF1 no es UTF-8 válido.
	got, err := decodeText([]byte{'a', 0xF1, 'o'})
	if err != nil {
		t.Fatalf("decodeText: %v", err)
	}
	if got != "año" {
		t.Errorf("decodeText = %q, se esperaba %q", got, "año")
	}
}

func TestDecodeTextRejectsBinary(t *testing.T) {
	if _, err := decodeText([]byte{0x25, 0x50, 0x00, 0x44}); err == nil {
		t.Error("decodeText aceptó contenido con bytes NUL")
	}
	if _, err := decodeText([]byte("   \n\n  ")); err == nil {
		t.Error("decodeText aceptó contenido vacío")
	}
}

func TestExtractPagesTextFile(t *testing.T) {
	pages, err := extractPages("servidor.log", []byte("ERROR conexión perdida\nWARN reintentando"))
	if err != nil {
		t.Fatalf("extractPages: %v", err)
	}
	if len(pages) != 1 || pages[0].Number != 0 {
		t.Fatalf("se esperaba una única página con Number 0, hay %+v", pages)
	}
	if !strings.Contains(pages[0].Text, "reintentando") {
		t.Errorf("texto extraído incompleto: %q", pages[0].Text)
	}
}
