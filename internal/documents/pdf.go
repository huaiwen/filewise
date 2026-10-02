package documents

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	pdfparser "github.com/ledongthuc/pdf"
)

type pdfInput struct {
	source    *bytes.Reader
	remaining int64
}

func (r *pdfInput) ReadAt(b []byte, off int64) (int, error) {
	r.remaining -= int64(len(b))
	if r.remaining < 0 {
		return 0, errors.New("PDF input work budget exceeded")
	}
	return r.source.ReadAt(b, off)
}

var pdfRefRE = regexp.MustCompile(`[0-9]+ [0-9]+ R`)

func pdfText(body []byte, out *Extraction) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("Invalid PDF structure")
		}
	}()
	reader, e := pdfparser.NewReader(&pdfInput{bytes.NewReader(body), 128 << 20}, int64(len(body)))
	if e != nil {
		return errors.New("Invalid or encrypted PDF")
	}
	trailer := reader.Trailer()
	if !trailer.Key("Encrypt").IsNull() {
		return errors.New("Encrypted PDFs require an unencrypted copy")
	}
	size := trailer.Key("Size").Int64()
	if size < 1 || size > 100000 {
		return errors.New("PDF page/object limits exceeded")
	}
	catalog := trailer.Key("Root")
	tree := catalog.Key("Pages")
	expected := tree.Key("Count").Int64()
	if expected < 1 || expected > 500 || tree.Key("Type").Name() != "Pages" {
		return errors.New("Invalid PDF page tree")
	}
	pages := []pdfparser.Page{}
	seen := map[string]bool{}
	nodes := 0
	var visit func(pdfparser.Value, int) (int, error)
	visit = func(v pdfparser.Value, depth int) (int, error) {
		nodes++
		if depth > 128 || nodes > 1000 {
			return 0, errors.New("PDF page tree limits exceeded")
		}
		if v.Key("Type").Name() == "Page" {
			pages = append(pages, pdfparser.Page{V: v})
			if len(pages) > 500 {
				return 0, errors.New("PDF page limits exceeded")
			}
			return 1, nil
		}
		if v.Key("Type").Name() != "Pages" {
			return 0, errors.New("Invalid PDF page tree")
		}
		kids := v.Key("Kids")
		if kids.Kind() != pdfparser.Array || kids.Len() > 1000 {
			return 0, errors.New("Invalid PDF page children")
		}
		representation := kids.String()
		refs := pdfRefRE.FindAllString(representation, -1)
		if len(refs) != kids.Len() || "["+strings.Join(refs, " ")+"]" != representation {
			return 0, errors.New("PDF page children must be indirect references")
		}
		count := 0
		for i, ref := range refs {
			if seen[ref] {
				return 0, errors.New("Repeated or cyclic PDF page tree")
			}
			seen[ref] = true
			n, e := visit(kids.Index(i), depth+1)
			if e != nil {
				return 0, e
			}
			count += n
		}
		if int64(count) != v.Key("Count").Int64() {
			return 0, errors.New("Incomplete PDF page tree")
		}
		return count, nil
	}
	count, e := visit(tree, 0)
	if e != nil {
		return e
	}
	if int64(count) != expected {
		return errors.New("Incomplete PDF page tree")
	}
	if !catalog.Key("AcroForm").IsNull() {
		out.note("form_fields_not_rendered")
	}
	total := 0
	for i, p := range pages {
		// Bound inherited resource lookup before entering the parser's parent walk.
		ancestor := p.V
		for depth := 0; !ancestor.IsNull(); depth++ {
			if depth > 128 {
				return errors.New("Cyclic PDF resource inheritance")
			}
			ancestor = ancestor.Key("Parent")
		}
		if len(p.Resources().Key("XObject").Keys()) > 0 {
			out.note("visual_or_embedded_content_omitted")
		}
		if p.V.Key("Annots").Len() > 0 {
			out.note("annotations_not_rendered")
		}
		text, e := p.GetPlainText(nil)
		if e != nil {
			out.note(fmt.Sprintf("page:%d:extraction_failed", i+1))
			continue
		}
		if len(text) > 4*maxText {
			return errors.New("PDF text limit exceeded")
		}
		if strings.TrimSpace(text) == "" {
			out.note(fmt.Sprintf("page:%d:no_text", i+1))
		}
		for line, s := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			if e = out.push(fmt.Sprintf("page:%d/line:%d", i+1, line+1), s, &total); e != nil {
				return e
			}
		}
	}
	return nil
}
