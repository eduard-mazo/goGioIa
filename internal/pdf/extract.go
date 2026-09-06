// Package pdf extracts plain text from PDF documents so it can be handed to a
// language model as context. It uses a pure-Go parser (no CGO required).
package pdf

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

var (
	newlines    = strings.NewReplacer("\r\n", "\n", "\r", "\n")
	trailingWS  = regexp.MustCompile(`[ \t]+\n`)
	blankRuns   = regexp.MustCompile(`\n{3,}`)
	multiSpaces = regexp.MustCompile(`[ \t]{2,}`)
	// dotLeaders son las líneas de puntos de los índices («Título ...... 32»):
	// páginas enteras de puntos producían chunks sin señal que contaminaban el
	// retrieval. 4+ puntos seguidos (con o sin espacios) no aparecen en prosa
	// ni en números de versión, así que se colapsan a un espacio.
	dotLeaders = regexp.MustCompile(`(?:\.[ \t]*){4,}`)
)

// normalize tidies the raw extractor output: normalises line endings, strips
// trailing whitespace, and collapses runs of blank lines / spaces. This trims
// wasted tokens without altering the document's wording.
func normalize(s string) string {
	s = newlines.Replace(s)
	s = dotLeaders.ReplaceAllString(s, " ")
	s = trailingWS.ReplaceAllString(s, "\n")
	s = multiSpaces.ReplaceAllString(s, " ")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Page is the extracted plain text of a single PDF page.
type Page struct {
	Number int
	Text   string
}

// ExtractPages returns the plain text of each page, preserving page numbers
// so RAG chunks can cite their source. Pages that fail to parse are skipped;
// an error is returned only when no page yields any text (e.g. scanned PDFs).
func ExtractPages(data []byte) (pages []Page, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("no se pudo leer el PDF (puede estar escaneado, cifrado o dañado): %v", r)
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("PDF inválido: %w", err)
	}

	total := reader.NumPage()
	for i := 1; i <= total; i++ {
		text := extractPageText(reader, i)
		pages = append(pages, Page{Number: i, Text: text})
	}
	pages = stripBoilerplate(pages)

	empty := true
	for _, p := range pages {
		if p.Text != "" {
			empty = false
			break
		}
	}
	if empty {
		return nil, fmt.Errorf("no se encontró texto (el PDF puede ser solo imagen / escaneado; aplique OCR externo antes de subirlo)")
	}
	return pages, nil
}

// stripBoilerplate elimina encabezados y pies de página: toda línea cuya
// forma normalizada se repite en buena parte de las páginas es plantilla de
// página, no contenido. Repetida en cada chunk, esa línea contamina el
// retrieval: todos los chunks del manual se parecen a cualquier consulta que
// mencione el nombre del producto.
func stripBoilerplate(pages []Page) []Page {
	if len(pages) < 4 {
		return pages
	}
	counts := make(map[string]int)
	for _, p := range pages {
		seen := make(map[string]bool)
		for _, line := range strings.Split(p.Text, "\n") {
			key := lineKey(line)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			counts[key]++
		}
	}
	threshold := max(3, len(pages)*2/5)
	out := make([]Page, 0, len(pages))
	for _, p := range pages {
		var b strings.Builder
		for _, line := range strings.Split(p.Text, "\n") {
			if key := lineKey(line); key != "" && counts[key] >= threshold {
				continue
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		out = append(out, Page{Number: p.Number, Text: strings.TrimSpace(b.String())})
	}
	return out
}

// lineKey normaliza una línea para compararla entre páginas: minúsculas, sin
// dígitos (los números de página cambian) y espacios colapsados. Las líneas
// largas nunca son encabezados y las muy cortas tras quitar dígitos no dan
// evidencia suficiente; ambas devuelven "" (no comparables).
func lineKey(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > 120 {
		return ""
	}
	var b strings.Builder
	for _, r := range line {
		if unicode.IsDigit(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	key := strings.Join(strings.Fields(b.String()), " ")
	if len(key) < 4 {
		return ""
	}
	return key
}

// extractPageText parses one page, converting parser panics into empty text
// so a single corrupt page doesn't abort the whole document.
func extractPageText(reader *pdf.Reader, n int) (text string) {
	defer func() {
		if recover() != nil {
			text = ""
		}
	}()
	page := reader.Page(n)
	if page.V.IsNull() {
		return ""
	}
	raw, err := page.GetPlainText(nil)
	if err != nil {
		return ""
	}
	return normalize(raw)
}

// ExtractText returns the concatenated plain text of every page in the PDF.
// The parser can panic on malformed files, so recovery is used to convert
// those into ordinary errors.
func ExtractText(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("no se pudo leer el PDF (puede estar escaneado, cifrado o dañado): %v", r)
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("PDF inválido: %w", err)
	}

	r, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("no se pudo extraer el texto del PDF: %w", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return "", err
	}

	out := normalize(buf.String())
	if out == "" {
		return "", fmt.Errorf("no se encontró texto (el PDF puede ser solo imagen / escaneado)")
	}
	return out, nil
}
