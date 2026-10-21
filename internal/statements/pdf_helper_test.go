package statements_test

import (
	"bytes"
	"fmt"
	"strings"
)

// makePDF builds a PDF whose pages hold the given lines of text in
// Helvetica, one under another; a page without lines has no text, like a
// scan.
func makePDF(pages [][]string) []byte {
	var objects []string
	add := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}
	catalog := add("") // filled in below
	pagesObj := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	var kids []string
	for _, lines := range pages {
		var content strings.Builder
		for i, line := range lines {
			escaped := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(line)
			fmt.Fprintf(&content, "BT /F1 10 Tf 40 %d Td (%s) Tj ET\n", 800-14*i, escaped)
		}
		stream := add(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()))
		page := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesObj, font, stream))
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
	}
	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesObj)
	objects[pagesObj-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, catalog, xref)
	return out.Bytes()
}
