package rag

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"goGioIa/internal/pdf"
)

// textMIME lista las extensiones de texto admitidas y su MIME. Cualquier
// archivo de estos tipos se ingiere como una sola «página» (Number 0: sin
// paginación) y se trocea igual que un PDF.
var textMIME = map[string]string{
	".txt":        "text/plain",
	".text":       "text/plain",
	".log":        "text/plain",
	".out":        "text/plain",
	".md":         "text/markdown",
	".markdown":   "text/markdown",
	".rst":        "text/plain",
	".adoc":       "text/plain",
	".csv":        "text/csv",
	".tsv":        "text/tab-separated-values",
	".json":       "application/json",
	".ndjson":     "application/x-ndjson",
	".xml":        "application/xml",
	".yaml":       "application/yaml",
	".yml":        "application/yaml",
	".toml":       "text/plain",
	".ini":        "text/plain",
	".conf":       "text/plain",
	".cfg":        "text/plain",
	".properties": "text/plain",
	".sql":        "application/sql",
	".sh":         "text/x-shellscript",
	".bat":        "text/plain",
	".ps1":        "text/plain",
	".py":         "text/x-python",
	".js":         "text/javascript",
	".ts":         "text/plain",
	".go":         "text/x-go",
	".java":       "text/x-java-source",
	".c":          "text/x-c",
	".h":          "text/x-c",
	".cpp":        "text/x-c",
	".html":       "text/html",
	".htm":        "text/html",
	".css":        "text/css",
}

// SupportedTypesMsg describe los formatos admitidos para mensajes de error.
const SupportedTypesMsg = "se admiten PDF y archivos de texto (.txt, .log, .md, .csv, .json, .xml, .yaml, .sql, código fuente…)"

// SupportedFile indica si el archivo puede ingerirse según su extensión.
func SupportedFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".pdf" {
		return true
	}
	_, ok := textMIME[ext]
	return ok
}

// MimeFor devuelve el MIME que se registra en la tabla documents.
func MimeFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".pdf" {
		return "application/pdf"
	}
	if m, ok := textMIME[ext]; ok {
		return m
	}
	return "text/plain"
}

// extractPages obtiene el texto plano del archivo según su tipo. Los PDF se
// extraen página a página (para citar «pág. N»); los archivos de texto se
// devuelven como una única página con Number 0, que significa «sin página».
func extractPages(fileName string, data []byte) ([]pdf.Page, error) {
	if strings.EqualFold(filepath.Ext(fileName), ".pdf") {
		return pdf.ExtractPages(data)
	}
	text, err := decodeText(data)
	if err != nil {
		return nil, err
	}
	return []pdf.Page{{Number: 0, Text: text}}, nil
}

// ExtractText devuelve el texto completo del archivo (PDF o texto plano).
// Lo usa el endpoint de adjuntos del chat, que no necesita paginación.
func ExtractText(fileName string, data []byte) (string, error) {
	if strings.EqualFold(filepath.Ext(fileName), ".pdf") {
		return pdf.ExtractText(data)
	}
	return decodeText(data)
}

var crNewlines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// blankRuns colapsa rachas largas de líneas en blanco sin tocar el resto del
// formato (la alineación de columnas de un log es útil en los snippets).
var blankRuns = regexp.MustCompile(`\n{3,}`)

// decodeText interpreta el contenido como texto: UTF-8 o, si no es válido,
// Latin-1 (logs y exports legacy en español). Rechaza contenido binario.
func decodeText(data []byte) (string, error) {
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("el archivo parece binario, no texto (%s)", SupportedTypesMsg)
	}
	var text string
	if utf8.Valid(data) {
		text = string(data)
	} else {
		// En Latin-1 todo byte es un punto de código válido.
		runes := make([]rune, len(data))
		for i, b := range data {
			runes[i] = rune(b)
		}
		text = string(runes)
	}
	text = strings.TrimSpace(blankRuns.ReplaceAllString(crNewlines.Replace(text), "\n\n"))
	if text == "" {
		return "", fmt.Errorf("el archivo no contiene texto")
	}
	return text, nil
}
