package documents

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/huaiwen/filewise/internal/testdocs"
	pdfparser "github.com/ledongthuc/pdf"
)

func TestSlidesDeclaredOrderNotesTablesAndRelationships(t *testing.T) {
	x := Extract("talk.pptx", testdocs.Slides("First declared slide", "Second declared slide", nil))
	if x.Info.Status != "text" || len(x.Fragments) != 3 || x.Fragments[0].Text != "First declared slide" || x.Fragments[1].Locator != "slide:1/notes/paragraph:1" || x.Fragments[2].Text != "Second declared slide" {
		t.Fatal(x)
	}
	table := `<p:sld xmlns:p="` + slideNS + `" xmlns:a="` + drawingNS + `"><a:tbl><a:tr><a:tc><a:p><a:r><a:t>cell</a:t></a:r></a:p></a:tc></a:tr></a:tbl></p:sld>`
	x = Extract("talk.pptx", testdocs.Slides("one", "two", map[string]string{"ppt/slides/slide2.xml": table}))
	if x.Fragments[0].Locator != "slide:1/table:1/row:1/cell:1/paragraph:1" {
		t.Fatal(x)
	}
	external := testdocs.Relations(`<Relationship Id="notes" Type="` + officeNS + `notesSlide" Target="https://example.invalid/private" TargetMode="External"/>`)
	for _, extra := range []map[string]string{{"ppt/slides/_rels/slide2.xml.rels": external}, {"ppt/slides/slide2.xml": "<invalid/>"}, {"ppt/_rels/presentation.xml.rels": testdocs.Relations(testdocs.Relationship("one", "slide", "missing.xml"))}} {
		x = Extract("bad.pptx", testdocs.Slides("one", "two", extra))
		if x.Info.Status != "failed" || len(x.Fragments) != 0 {
			t.Fatal(x)
		}
	}
	x = Extract("partial.pptx", testdocs.Slides("one", "two", map[string]string{"ppt/media/image.bin": "not text"}))
	if x.Info.Status != "partial" {
		t.Fatal(x)
	}
}
func TestWorkbookExactCellsFormulaOriginsDatesAndFullTable(t *testing.T) {
	headers := `<row r="1"><c r="A1" t="inlineStr"><is><t>id</t></is></c><c r="B1" t="inlineStr"><is><t>amount</t></is></c></row>`
	rows := headers + `<row r="2"><c r="A2" t="s"><v>0</v></c><c r="B2"><v>9007199254740993.100000000000000001</v></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>formula</t></is></c><c r="B3"><f t="shared" ref="B3:B4" si="0">B2+1</f><v>123</v></c></row><row r="4"><c r="A4" t="inlineStr"><is><t>inherited formula</t></is></c><c r="B4"><v>999</v></c></row><row r="5"><c r="A5" t="b"><v>1</v></c><c r="B5" s="1"><v>45292.5</v></c></row>`
	x := Extract("data.xlsx", testdocs.Workbook(rows, nil))
	if x.Info.Status != "partial" || len(x.Sheets) != 1 || x.Sheets[0].Cells[2].Value != "joined text" || x.Sheets[0].Cells[3].Value != "9007199254740993.100000000000000001" {
		t.Fatal(x)
	}
	table, columns, e := Table(x, 1)
	if e != nil || len(table) != 4 || len(columns) != 2 || table[0]["amount"] != "9007199254740993.100000000000000001" || table[1]["amount"] != nil || table[2]["amount"] != nil || table[3]["id"] != true {
		t.Fatalf("%v %v %v", table, columns, e)
	}
	date := table[3]["amount"].(map[string]any)
	if date["excel_serial"] != "45292.5" || date["date_system"] != 1904 {
		t.Fatal(date)
	}
	sparse := Extract("sparse.xlsx", testdocs.Workbook(headers+`<row r="5"><c r="A5" t="inlineStr"><is><t>last</t></is></c><c r="B5"><v>1</v></c></row>`, nil))
	records, _, e := Table(sparse, 1)
	if e != nil || len(records) != 4 || len(records[0]) != 0 {
		t.Fatal(records, e)
	}
	if _, _, e = Table(x, 2); e == nil {
		t.Fatal("missing sheet accepted")
	}
}
func TestWorkbookRejectsMalformedCellsHeadersAndResources(t *testing.T) {
	for name, cell := range map[string]string{"address": "<c r=\"a2\"><v>1</v></c>", "row": `<c r="A3"><v>1</v></c>`, "column": `<c r="XFD2"><v>1</v></c>`, "bool": `<c r="A2" t="b"><v>true</v></c>`, "number": `<c r="A2"><v>1e10000000</v></c>`, "shared": `<c r="A2" t="s"><v>1</v></c>`, "style": `<c r="A2" s="99"><v>1</v></c>`, "range": `<c r="A2"><f ref="A3:A1">1</f><v>1</v></c>`, "duplicate": `<c r="A2"><v>1</v></c><c r="A2"><v>2</v></c>`} {
		t.Run(name, func(t *testing.T) {
			x := Extract("data.xlsx", testdocs.Workbook(`<row r="2">`+cell+`</row>`, nil))
			if x.Info.Status != "failed" || len(x.Sheets) != 0 || len(x.Fragments) != 0 {
				t.Fatal(x)
			}
		})
	}
	for _, header := range []string{`<c r="A1"><v>1</v></c>`, `<c r="A1" t="str"><f>"id"</f><v>id</v></c>`, `<c r="A1" t="inlineStr"><is><t>id</t></is></c><c r="B1" t="inlineStr"><is><t>id</t></is></c>`} {
		x := Extract("data.xlsx", testdocs.Workbook(`<row r="1">`+header+`</row>`, nil))
		if _, _, e := Table(x, 1); e == nil {
			t.Fatal("nonliteral or duplicate header accepted", x)
		}
	}
	x := Extract("data.xlsx", testdocs.Workbook(`<row r="1"><c r="A1" t="s"><v>0</v></c></row>`, map[string]string{"xl/_rels/workbook.xml.rels": testdocs.Relations(testdocs.Relationship("sheet", "worksheet", "worksheets/sheet1.xml") + `<Relationship Id="strings" Type="` + officeNS + `sharedStrings" Target="https://example.invalid/a" TargetMode="External"/>`)}))
	if x.Info.Status != "failed" {
		t.Fatal(x)
	}
	var rows strings.Builder
	for i := 1; i <= 20001; i++ {
		fmt.Fprintf(&rows, `<row r="%d"><c r="A%d"><v>1</v></c></row>`, i, i)
	}
	x = Extract("excess.xlsx", testdocs.Workbook(rows.String(), nil))
	if x.Info.Status != "failed" {
		t.Fatal("cell limit ignored")
	}
}
func TestPDFPagesEncryptionAndTreeIntegrity(t *testing.T) {
	body := testdocs.PDF("First page", "Second page")
	x := Extract("paper.pdf", body)
	if x.Info.Status != "text" || len(x.Fragments) != 2 || x.Fragments[0].Text != "First page" || !strings.HasPrefix(x.Fragments[1].Locator, "page:2/line:") {
		t.Fatal(x)
	}
	chinese := Extract("chinese.pdf", testdocs.ChinesePDF())
	if chinese.Info.Status != "text" || len(chinese.Fragments) != 1 || chinese.Fragments[0].Text != "机器故障" {
		t.Fatal(chinese)
	}
	x = Extract("mixed.pdf", testdocs.PDF("First page", ""))
	if x.Info.Status != "partial" || len(x.Fragments) != 1 {
		t.Fatal(x)
	}
	x = Extract("blank.pdf", testdocs.PDF(""))
	if x.Info.Status != "no_text" || len(x.Fragments) != 0 {
		t.Fatal(x)
	}
	encrypted := testdocs.EncryptedPDF()
	reader, e := pdfparser.NewReader(bytes.NewReader(encrypted), int64(len(encrypted)))
	if e != nil {
		t.Fatalf("fixture must authenticate with an empty password: %v", e)
	}
	if reader.Trailer().Key("Encrypt").IsNull() || reader.NumPage() != 1 {
		t.Fatal("encrypted fixture lost its encryption or page tree")
	}
	x = Extract("encrypted.pdf", encrypted)
	if x.Info.Status != "failed" || !strings.Contains(x.Info.Error, "Encrypted") {
		t.Fatal(x)
	}
	for name, b := range map[string][]byte{"count": bytes.Replace(body, []byte("/Count 2"), []byte("/Count 9"), 1), "repeated": bytes.Replace(body, []byte("/Kids [3 0 R 5 0 R]"), []byte("/Kids [3 0 R 3 0 R]"), 1), "cycle": bytes.Replace(body, []byte("/Kids [3 0 R 5 0 R]"), []byte("/Kids [2 0 R 5 0 R]"), 1), "bad": []byte("%PDF-1.7\nbroken\n%%EOF")} {
		x = Extract(name+".pdf", b)
		if x.Info.Status != "failed" || len(x.Fragments) != 0 {
			t.Fatal(name, x)
		}
	}
}
