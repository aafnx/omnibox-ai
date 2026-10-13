package attach

import (
	"os"
	"strings"
	"testing"
)

// A PDF gives its text; a file that is not a PDF, or one without text, says so.
func TestPDF(t *testing.T) {
	data, err := os.ReadFile("testdata/contract.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if KindOf("Договор.PDF") != PDF || Needs(PDF) != "" {
		t.Fatal("a PDF is a text kind")
	}
	text, err := ExtractText("Договор.pdf", data)
	if err != nil || !strings.Contains(text, "Договор купли-продажи № 15/10") || !strings.Contains(text, "паспорт серии 9204 № 123456") {
		t.Fatalf("%v %q", err, text)
	}
	if _, err := ExtractText("x.pdf", []byte("%PDF-1.4 garbage")); err == nil || !strings.Contains(err.Error(), "повреждён") {
		t.Fatal(err)
	}
	if _, err := pdfText(blankPDF); err == nil || !strings.Contains(err.Error(), "скан") {
		t.Fatalf("a PDF without text: %v", err)
	}
}

// blankPDF is one empty page: what a scan looks like to a text reader.
var blankPDF = []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]>>endobj\n" +
	"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000101 00000 n \ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n164\n%%EOF")
