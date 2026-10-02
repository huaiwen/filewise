// Package testdocs builds small synthetic originals for parser and native-runtime
// acceptance tests. It is not linked into the Filewise executable.
package testdocs

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
	"sort"
	"strings"
)

const Rel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
const PackageRel = "http://schemas.openxmlformats.org/package/2006/relationships"
const Excel = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
const PowerPoint = "http://schemas.openxmlformats.org/presentationml/2006/main"
const Drawing = "http://schemas.openxmlformats.org/drawingml/2006/main"

func Relations(body string) string {
	return `<Relationships xmlns="` + PackageRel + `">` + body + `</Relationships>`
}
func Relationship(id, kind, target string) string {
	return `<Relationship Id="` + id + `" Type="` + Rel + kind + `" Target="` + target + `"/>`
}
func Office(main string, parts map[string]string) []byte {
	all := map[string]string{"_rels/.rels": Relations(Relationship("main", "officeDocument", main))}
	for k, v := range parts {
		all[k] = v
	}
	names := []string{}
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, n := range names {
		w, e := z.Create(n)
		if e != nil {
			panic(e)
		}
		if _, e = w.Write([]byte(all[n])); e != nil {
			panic(e)
		}
	}
	if e := z.Close(); e != nil {
		panic(e)
	}
	return b.Bytes()
}
func Workbook(rows string, extra map[string]string) []byte {
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="` + Excel + `" xmlns:r="` + strings.TrimSuffix(Rel, "/") + `"><workbookPr date1904="1"/><sheets><sheet name="Data" sheetId="1" r:id="sheet"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": Relations(Relationship("sheet", "worksheet", "worksheets/sheet1.xml") + Relationship("strings", "sharedStrings", "sharedStrings.xml") + Relationship("styles", "styles", "styles.xml")),
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="` + Excel + `"><dimension ref="A1:XFD1048576"/><sheetData>` + rows + `</sheetData></worksheet>`,
		"xl/sharedStrings.xml":       `<sst xmlns="` + Excel + `"><si><r><t>joined </t></r><r><t>text</t></r><rPh><t>ignored pronunciation</t></rPh></si></sst>`,
		"xl/styles.xml":              `<styleSheet xmlns="` + Excel + `"><cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>`,
	}
	for k, v := range extra {
		parts[k] = v
	}
	return Office("xl/workbook.xml", parts)
}
func Slides(first, second string, extra map[string]string) []byte {
	paragraph := func(text string) string { return `<a:p><a:r><a:t>` + text + `</a:t></a:r></a:p>` }
	slide := func(text string) string {
		return `<p:sld xmlns:p="` + PowerPoint + `" xmlns:a="` + Drawing + `"><p:cSld><p:spTree><p:sp><p:txBody>` + paragraph(text) + `</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	parts := map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="` + PowerPoint + `" xmlns:r="` + strings.TrimSuffix(Rel, "/") + `"><p:sldIdLst><p:sldId id="256" r:id="two"/><p:sldId id="257" r:id="one"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": Relations(Relationship("one", "slide", "slides/slide1.xml") + Relationship("two", "slide", "slides/slide2.xml")),
		"ppt/slides/slide1.xml":           slide(second), "ppt/slides/slide2.xml": slide(first),
		"ppt/slides/_rels/slide2.xml.rels": Relations(Relationship("notes", "notesSlide", "../notesSlides/notesSlide1.xml")),
		"ppt/notesSlides/notesSlide1.xml":  `<p:notes xmlns:p="` + PowerPoint + `" xmlns:a="` + Drawing + `">` + paragraph("Speaker note") + `</p:notes>`,
	}
	for k, v := range extra {
		parts[k] = v
	}
	return Office("ppt/presentation.xml", parts)
}
func pdfObjects(objects []string, trailer string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := []int{0}
	for i, s := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, s)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R %s >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), trailer, xref)
	return b.Bytes()
}
func PDF(pages ...string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	kids := []string{}
	for _, text := range pages {
		pageID := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageID))
		stream := ""
		if text != "" {
			text = strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
			stream = "BT /F1 12 Tf 20 100 Td (" + text + ") Tj ET"
		}
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> /Contents %d 0 R >>", pageID+1), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), strings.Join(kids, " "))
	return pdfObjects(objects, "")
}

func ChinesePDF() []byte {
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def /CMapName /Synthetic def /CMapType 2 def 1 begincodespacerange <0000> <FFFF> endcodespacerange 4 beginbfchar <0001> <673A> <0002> <5668> <0003> <6545> <0004> <969C> endbfchar endcmap CMapName currentdict /CMap defineresource pop end end"
	stream := "BT /F1 12 Tf 20 100 Td <0001000200030004> Tj ET"
	return pdfObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type0 /BaseFont /Synthetic /Encoding /Identity-H /DescendantFonts [7 0 R] /ToUnicode 6 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cmap), cmap),
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Synthetic /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /DW 1000 /FontDescriptor 8 0 R >>",
		"<< /Type /FontDescriptor /FontName /Synthetic /Flags 4 /FontBBox [0 -200 1000 1000] /ItalicAngle 0 /Ascent 880 /Descent -120 /CapHeight 880 /StemV 80 >>",
	}, "")
}

// Empty-password encryption is deliberately readable without a password by many
// PDF libraries. Filewise must still reject it rather than treating it as plain.
func EncryptedPDF() []byte {
	padding := []byte{0x28, 0xbf, 0x4e, 0x5e, 0x4e, 0x75, 0x8a, 0x41, 0x64, 0, 0x4e, 0x56, 0xff, 0xfa, 1, 8, 0x2e, 0x2e, 0, 0xb6, 0xd0, 0x68, 0x3e, 0x80, 0x2f, 0x0c, 0xa9, 0xfe, 0x64, 0x53, 0x69, 0x7a}
	crypt := func(key, body []byte) []byte {
		cipher, e := rc4.NewCipher(key)
		if e != nil {
			panic(e)
		}
		out := make([]byte, len(body))
		cipher.XORKeyStream(out, body)
		return out
	}
	sum := md5.Sum(padding)
	owner := crypt(sum[:5], padding)
	id := []byte("filewise-test-id!")
	keyInput := append(append(append(append([]byte{}, padding...), owner...), 0xfc, 0xff, 0xff, 0xff), id...)
	keySum := md5.Sum(keyInput)
	key := keySum[:5]
	user := crypt(key, padding)
	streamKey := md5.Sum(append(append([]byte{}, key...), 4, 0, 0, 0, 0))
	stream := crypt(streamKey[:10], []byte("BT /F1 12 Tf (Encrypted text) Tj ET"))
	return pdfObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Count 1 /Kids [3 0 R] >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> /Contents 4 0 R >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream), fmt.Sprintf("<< /Filter /Standard /V 1 /R 2 /Length 40 /O <%x> /U <%x> /P -4 >>", owner, user)}, fmt.Sprintf("/Encrypt 5 0 R /ID [<%x> <%x>]", id, id))
}
