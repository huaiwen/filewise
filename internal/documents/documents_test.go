package documents

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/huaiwen/filewise/internal/testdocs"
)

func packageBytes(t *testing.T, body string, extra map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	parts := map[string]string{"_rels/.rels": `<Relationships xmlns="` + relNS + `"><Relationship Id="main" Type="` + officeNS + `officeDocument" Target="word/document.xml"/></Relationships>`, "word/document.xml": `<w:document xmlns:w="` + wordNS + `"><w:body>` + body + `</w:body></w:document>`}
	for k, v := range extra {
		parts[k] = v
	}
	for k, v := range parts {
		w, err := z.Create(k)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestWordRealExamplesAndLocations(t *testing.T) {
	for _, locale := range []string{"zh", "en"} {
		for _, version := range []string{"before", "after"} {
			body, err := os.ReadFile("../../site/examples/acceptance-" + locale + "-" + version + ".docx")
			if err != nil {
				t.Fatal(err)
			}
			e := Extract("acceptance.docx", body)
			if e.Info.Status != "text" || len(e.Fragments) != 4 {
				t.Fatalf("%s/%s: %+v", locale, version, e)
			}
			want := "100 kPa"
			if version == "after" {
				want = "120 kPa"
			}
			if e.Fragments[2].Locator != "word/document.xml/paragraph:3" || !strings.Contains(e.Fragments[2].Text, want) {
				t.Fatal(e)
			}
		}
	}
	body := packageBytes(t, `<w:p><w:r><w:t>Joined</w:t><w:tab/><w:t>runs</w:t></w:r><w:del><w:r><w:delText>removed</w:delText></w:r></w:del><w:r><w:instrText>not executed</w:instrText></w:r></w:p><w:del><w:p><w:r><w:t>deleted paragraph</w:t></w:r></w:p></w:del><w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`, map[string]string{
		"word/_rels/document.xml.rels": `<Relationships xmlns="` + relNS + `"><Relationship Id="header" Type="` + officeNS + `header" Target="header1.xml"/></Relationships>`,
		"word/header1.xml":             `<w:hdr xmlns:w="` + wordNS + `"><w:p><w:r><w:t>header</w:t></w:r></w:p></w:hdr>`,
	})
	e := Extract("sample.docx", body)
	if e.Info.Status != "text" || len(e.Fragments) != 3 || e.Fragments[0].Text != "Joined\truns" || e.Fragments[1].Locator != "word/document.xml/table:1/row:1/cell:1/paragraph:3" || e.Fragments[2].Locator != "word/header1.xml/paragraph:1" {
		t.Fatalf("%+v", e)
	}
	strict := packageBytes(t, "", map[string]string{"word/document.xml": `<w:document xmlns:w="` + strictWordNS + `"><w:body><w:p><w:r><w:t>strict</w:t></w:r></w:p></w:body></w:document>`})
	if e := Extract("strict.docx", strict); e.Info.Status != "text" || e.Fragments[0].Text != "strict" {
		t.Fatal(e)
	}
}
func TestWordRejectsHostilePackagesAndReportsMissingCoverage(t *testing.T) {
	good := `<w:p><w:r><w:t>body</w:t></w:r></w:p>`
	cases := map[string]map[string]string{
		"traversal": {"../outside.xml": "<x/>"}, "DTD": {"unused.xml": `<!DOCTYPE x [<!ENTITY secret SYSTEM "file:///etc/passwd">]><x>&secret;</x>`},
		"malformed": {"unused.xml": "<x>"}, "multiple-roots": {"unused.xml": "<x/><y/>"}, "duplicate-attribute": {"unused.xml": `<x a="1" a="2"/>`},
		"missing-main":              {"_rels/.rels": `<Relationships xmlns="` + relNS + `"/>`},
		"external-main":             {"_rels/.rels": `<Relationships xmlns="` + relNS + `"><Relationship Id="x" Type="` + officeNS + `officeDocument" Target="https://example.invalid/data" TargetMode="External"/></Relationships>`},
		"escaped-relationship":      {"_rels/.rels": `<Relationships xmlns="` + relNS + `"><Relationship Id="x" Type="` + officeNS + `officeDocument" Target="../outside.xml"/></Relationships>`},
		"external-header":           {"word/_rels/document.xml.rels": `<Relationships xmlns="` + relNS + `"><Relationship Id="x" Type="` + officeNS + `header" Target="https://example.invalid/data" TargetMode="External"/></Relationships>`},
		"missing-header":            {"word/_rels/document.xml.rels": `<Relationships xmlns="` + relNS + `"><Relationship Id="x" Type="` + officeNS + `header" Target="missing.xml"/></Relationships>`},
		"whole-package-node-budget": {"one.xml": "<x>" + strings.Repeat("<x/>", 110000) + "</x>", "two.xml": "<x>" + strings.Repeat("<x/>", 110000) + "</x>"},
		"deep-xml":                  {"unused.xml": strings.Repeat("<x>", 129) + strings.Repeat("</x>", 129)},
		"large-xml":                 {"unused.xml": "<x>" + strings.Repeat("a", maxXML) + "</x>"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			e := Extract("x.docx", packageBytes(t, good, extra))
			if e.Info.Status != "failed" || len(e.Fragments) != 0 {
				t.Fatal(e)
			}
		})
	}
	var dup bytes.Buffer
	z := zip.NewWriter(&dup)
	for range 2 {
		w, err := z.Create("x.xml")
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("<x/>"))
	}
	z.Close()
	if e := Extract("x.docx", dup.Bytes()); e.Info.Status != "failed" || e.Info.Error != "Duplicate ZIP part" {
		t.Fatal(e)
	}
	for _, extra := range []map[string]string{{"word/media/image.bin": "fake image"}, {"word/embeddings/data.bin": "fake embedding"}} {
		e := Extract("x.docx", packageBytes(t, good, extra))
		if e.Info.Status != "partial" {
			t.Fatal(e)
		}
	}
	for _, omitted := range []string{`<w:altChunk/>`, `<w:p><w:r><w:drawing/></w:r></w:p>`, `<w:p><w:fldSimple><w:r><w:t>cached</w:t></w:r></w:fldSimple></w:p>`, `<w:p><w:pPr><w:numPr/></w:pPr></w:p>`} {
		if e := Extract("x.docx", packageBytes(t, good+omitted, nil)); e.Info.Status != "partial" {
			t.Fatal(e)
		}
	}
	if e := Extract("x.docx", packageBytes(t, "", nil)); e.Info.Status != "no_text" {
		t.Fatal(e)
	}
	if e := Extract("x.pdf", []byte("%PDF-")); e.Info.Status != "failed" {
		t.Fatal(e)
	}
	if e := Extract("x.txt", []byte("%PDF-1.7\n")); e.Info.Status != "failed" {
		t.Fatal(e)
	}
	if e := Extract("x.txt", []byte{0xff}); e.Info.Status != "failed" {
		t.Fatal(e)
	}
	if e := Extract("x.md", []byte("first\n\nthird")); e.Info.Status != "text" || len(e.Fragments) != 2 || e.Fragments[1].Locator != "line:3" {
		t.Fatal(e)
	}
}

func FuzzExtract(f *testing.F) {
	seed, err := os.ReadFile("../../site/examples/acceptance-zh-after.docx")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add(testdocs.Slides("First", "Second", nil))
	f.Add(testdocs.Workbook(`<row r="1"><c r="A1" t="inlineStr"><is><t>header</t></is></c></row>`, nil))
	f.Add([]byte("invalid"))
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		for _, format := range []string{"docx", "xlsx", "pptx"} {
			e := Extract("sample."+format, body)
			if e.Info.FragmentCount != len(e.Fragments) || len(e.Fragments) > maxFragments {
				t.Fatal("unbounded output")
			}
			if e.Info.Status == "failed" && (len(e.Fragments) != 0 || len(e.Sheets) != 0) {
				t.Fatal("failed text became evidence")
			}
		}
	})
}
