// Package pdf extracts plain text from PDF documents so it can be handed to a
// language model as context. It uses a pure-Go parser (no CGO required).
package pdf

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ledongthuc/pdf"
)

var (
	newlines    = strings.NewReplacer("\r\n", "\n", "\r", "\n")
	trailingWS  = regexp.MustCompile(`[ \t]+\n`)
	blankRuns   = regexp.MustCompile(`\n{3,}`)
	multiSpaces = regexp.MustCompile(`[ \t]{2,}`)
)

// normalize tidies the raw extractor output: normalises line endings, strips
// trailing whitespace, and collapses runs of blank lines / spaces. This trims
// wasted tokens without altering the document's wording.
func normalize(s string) string {
	s = newlines.Replace(s)
	s = trailingWS.ReplaceAllString(s, "\n")
	s = multiSpaces.ReplaceAllString(s, " ")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
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
