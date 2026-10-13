package attach

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// maxPDFPages bounds the pages read from one PDF: a book-length file gives its beginning.
const maxPDFPages = 500

// errScan is a PDF without text: pages scanned as pictures need recognition, which OmniBox does
// not do on the PC.
var errScan = errors.New("в PDF нет текста — похоже, это скан или фото страниц. Сохраните документ с текстовым слоем (например, из Word) или приложите страницы как фото для модели со зрением")

// pdfText reads the text of a PDF page by page. The reader is pure Go; a broken file that makes
// it panic is reported as unreadable, not taken down with the service.
func pdfText(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", errors.New("файл PDF повреждён или зашифрован")
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("файл PDF повреждён или зашифрован")
	}
	var b strings.Builder
	n := min(r.NumPage(), maxPDFPages)
	for i := 1; i <= n && b.Len() < MaxText*4; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		t, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		if t = strings.TrimSpace(t); t != "" {
			if n > 1 {
				fmt.Fprintf(&b, "[Страница %d]\n", i)
			}
			b.WriteString(t + "\n\n")
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", errScan
	}
	if r.NumPage() > maxPDFPages {
		fmt.Fprintf(&b, "(Показаны первые %d страниц из %d.)", maxPDFPages, r.NumPage())
	}
	return b.String(), nil
}
