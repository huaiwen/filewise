// Package documents extracts bounded text and concrete source locations without
// executing Office code, evaluating fields, or fetching external relationships.
package documents

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxFile        = 10 << 20
	maxXML         = 8 << 20
	maxExpanded    = 32 << 20
	maxText        = 2_000_000
	maxFragments   = 20_000
	wordNS         = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	strictWordNS   = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	relNS          = "http://schemas.openxmlformats.org/package/2006/relationships"
	officeNS       = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	strictOfficeNS = "http://purl.oclc.org/ooxml/officeDocument/relationships/"
)

type Fragment struct {
	Locator string `json:"locator"`
	Text    string `json:"text"`
}
type Info struct {
	Format        string   `json:"format"`
	Status        string   `json:"status"`
	Engine        string   `json:"engine"`
	Scope         string   `json:"scope"`
	Notes         []string `json:"notes"`
	FragmentCount int      `json:"fragment_count"`
	Error         string   `json:"error,omitempty"`
}
type Extraction struct {
	Info      Info       `json:"info"`
	Fragments []Fragment `json:"fragments"`
	Sheets    []Sheet    `json:"sheets,omitempty"`
}

func Hash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func extract(name string, body []byte) Extraction {
	format := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	out := Extraction{Info: Info{Format: format, Status: "text", Engine: "filewise-go-documents-v1", Scope: "text_only; no OCR or visual layout", Notes: []string{}}, Fragments: []Fragment{}}
	var err error
	if len(body) > MaxFile {
		err = errors.New("File exceeds 10 MiB")
	} else {
		switch format {
		case "docx":
			err = word(body, &out)
		case "pptx":
			err = slides(body, &out)
		case "xlsx":
			err = workbook(body, &out)
		case "pdf":
			err = pdfText(body, &out)
		case "txt", "md", "csv", "json", "log", "":
			if !utf8.Valid(body) || bytes.ContainsRune(body, 0) || bytes.HasPrefix(body, []byte("%PDF-")) || bytes.HasPrefix(body, []byte("PK\x03\x04")) {
				err = errors.New("Not supported UTF-8 text")
			} else {
				total := 0
				for i, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
					if err = out.push(fmt.Sprintf("line:%d", i+1), line, &total); err != nil {
						break
					}
				}
			}
		default:
			out.Info.Status = "unsupported"
			out.Info.Notes = append(out.Info.Notes, "unsupported_format; originals_are_preserved")
		}
	}
	if err != nil {
		out.Info.Status = "failed"
		out.Info.Error = err.Error()
		out.Fragments = []Fragment{}
		out.Sheets = nil
	}
	if out.Info.Status == "text" {
		if len(out.Fragments) == 0 {
			out.Info.Status = "no_text"
		} else if len(out.Info.Notes) > 0 {
			out.Info.Status = "partial"
		}
	}
	out.Info.FragmentCount = len(out.Fragments)
	return out
}
func (e *Extraction) note(s string) {
	for _, v := range e.Info.Notes {
		if v == s {
			return
		}
	}
	e.Info.Notes = append(e.Info.Notes, s)
}
func (e *Extraction) push(locator, text string, total *int) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	*total += utf8.RuneCountInString(text)
	if *total > maxText || len(e.Fragments) >= maxFragments || len(locator) > 256 {
		return errors.New("Document text or fragment limit exceeded")
	}
	if strings.ContainsRune(text, '\ufffd') {
		e.note("undecodable_text")
	}
	e.Fragments = append(e.Fragments, Fragment{locator, text})
	return nil
}
func safePart(name string) bool {
	return fs.ValidPath(name) && !strings.ContainsAny(name, "\\:\x00") && !strings.HasPrefix(name, "/")
}
func wordTag(n xml.Name, local string) bool {
	return n.Local == local && (n.Space == wordNS || n.Space == strictWordNS)
}

type node struct {
	Name     xml.Name
	Attr     []xml.Attr
	Children []*node
	Text     strings.Builder
	Parent   *node
}

// XML trees are bounded by part bytes, element count, depth and expanded text.
func parseXML(body []byte, budget *int) (*node, error) {
	if !utf8.Valid(body) || len(body) > maxXML {
		return nil, errors.New("Office XML must be bounded UTF-8")
	}
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})))
	var root *node
	stack := []*node{}
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("Invalid Office XML")
		}
		switch v := token.(type) {
		case xml.Directive:
			return nil, errors.New("Office XML directives and DTDs are not accepted")
		case xml.StartElement:
			*budget -= 1 + len(v.Attr)
			if *budget < 0 || len(stack) >= 128 {
				return nil, errors.New("Office XML node/depth limit exceeded")
			}
			attrs := map[xml.Name]bool{}
			for _, a := range v.Attr {
				if attrs[a.Name] {
					return nil, errors.New("Duplicate XML attribute")
				}
				attrs[a.Name] = true
			}
			n := &node{Name: v.Name, Attr: v.Attr}
			if len(stack) > 0 {
				n.Parent = stack[len(stack)-1]
				n.Parent.Children = append(n.Parent.Children, n)
			} else {
				if root != nil {
					return nil, errors.New("Multiple XML roots")
				}
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("Unbalanced XML")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				n := stack[len(stack)-1]
				n.Text.Write(v)
			} else if strings.TrimSpace(string(v)) != "" {
				return nil, errors.New("Text outside XML root")
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errors.New("Missing XML root")
	}
	return root, nil
}
func walk(n *node, f func(*node)) {
	f(n)
	for _, child := range n.Children {
		walk(child, f)
	}
}
func attr(n *node, name string) string {
	for _, a := range n.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

type relationship struct {
	Kind     string
	Target   string
	External bool
}
type officePackage struct {
	Parts map[string]*node
	Notes []string
}

func openPackage(body []byte) (*officePackage, error) {
	z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, errors.New("Invalid Office ZIP")
	}
	if len(z.File) > 4096 {
		return nil, errors.New("Office ZIP part limit exceeded")
	}
	p := &officePackage{Parts: map[string]*node{}}
	names := map[string]bool{}
	var total uint64
	expanded := 0
	xmlBudget := 200_000 // Shared across all parts; many small XML parts cannot bypass it.
	for _, f := range z.File {
		name := f.Name
		if names[name] {
			return nil, errors.New("Duplicate ZIP part")
		}
		names[name] = true
		if !safePart(strings.TrimSuffix(name, "/")) || f.Flags&1 != 0 || f.Mode()&fs.ModeSymlink != 0 || (f.Mode()&fs.ModeType != 0 && !f.FileInfo().IsDir()) {
			return nil, errors.New("Unsafe Office ZIP part")
		}
		if f.UncompressedSize64 > maxExpanded || total > maxExpanded-f.UncompressedSize64 {
			return nil, errors.New("Expanded Office ZIP limit exceeded")
		}
		total += f.UncompressedSize64
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.Contains(name, "/media/") || strings.Contains(name, "/embeddings/") || strings.Contains(name, "/charts/") || strings.Contains(name, "/diagrams/") || strings.HasSuffix(name, "vbaProject.bin") {
			p.Notes = append(p.Notes, "visual_or_embedded_content_omitted")
		}
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".rels") {
			continue
		}
		if f.UncompressedSize64 > maxXML {
			return nil, errors.New("Office XML exceeds 8 MiB")
		}
		r, err := f.Open()
		if err != nil {
			return nil, errors.New("Unsupported Office ZIP compression")
		}
		data, err := io.ReadAll(io.LimitReader(r, maxXML+1))
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			return nil, errors.New("Office ZIP checksum/decompression failed")
		}
		expanded += len(data)
		if len(data) > maxXML || expanded > maxExpanded {
			return nil, errors.New("Expanded Office XML limit exceeded")
		}
		doc, err := parseXML(data, &xmlBudget)
		if err != nil {
			return nil, err
		}
		p.Parts[name] = doc
	}
	return p, nil
}
func resolveTarget(base, target string) (string, error) {
	if target == "" || strings.ContainsAny(target, "\\:?#%\x00") {
		return "", errors.New("Unsupported Office relationship target")
	}
	parts := []string{}
	if !strings.HasPrefix(target, "/") && strings.Contains(base, "/") {
		parts = strings.Split(path.Dir(base), "/")
	}
	for _, s := range strings.Split(strings.TrimPrefix(target, "/"), "/") {
		switch s {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return "", errors.New("Office relationship escapes ZIP")
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, s)
		}
	}
	result := strings.Join(parts, "/")
	if !safePart(result) {
		return "", errors.New("Invalid Office relationship path")
	}
	return result, nil
}
func (p *officePackage) relations(part string) (map[string]relationship, error) {
	name := "_rels/.rels"
	if part != "" {
		name = path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
	}
	result := map[string]relationship{}
	root := p.Parts[name]
	if root == nil {
		return result, nil
	}
	if root.Name.Space != relNS || root.Name.Local != "Relationships" {
		return nil, errors.New("Invalid Office relationship document")
	}
	for _, n := range root.Children {
		if n.Name.Space != relNS || n.Name.Local != "Relationship" {
			continue
		}
		id, kind, target := attr(n, "Id"), attr(n, "Type"), attr(n, "Target")
		if id == "" || kind == "" || target == "" {
			return nil, errors.New("Missing Office relationship attribute")
		}
		if _, ok := result[id]; ok {
			return nil, errors.New("Duplicate Office relationship ID")
		}
		external := attr(n, "TargetMode") == "External"
		if !external {
			var err error
			target, err = resolveTarget(part, target)
			if err != nil {
				return nil, err
			}
		}
		result[id] = relationship{kind, target, external}
	}
	return result, nil
}
func relationKind(kind, name string) bool {
	return kind == officeNS+name || kind == strictOfficeNS+name
}
func word(body []byte, out *Extraction) error {
	p, err := openPackage(body)
	if err != nil {
		return err
	}
	for _, n := range p.Notes {
		out.note(n)
	}
	rels, err := p.relations("")
	if err != nil {
		return err
	}
	main := ""
	for _, r := range rels {
		if relationKind(r.Kind, "officeDocument") {
			if main != "" || r.External {
				return errors.New("Invalid Office main relationship")
			}
			main = r.Target
		}
	}
	root := p.Parts[main]
	if root == nil || !wordTag(root.Name, "document") {
		return errors.New("Word main document missing")
	}
	total := 0
	if err = paragraphs(root, main, out, &total); err != nil {
		return err
	}
	rels, err = p.relations(main)
	if err != nil {
		return err
	}
	related := map[string]bool{}
	for _, r := range rels {
		for _, kind := range []string{"header", "footer", "footnotes", "endnotes"} {
			if relationKind(r.Kind, kind) {
				if r.External {
					return errors.New("External Word text part")
				}
				if p.Parts[r.Target] == nil {
					return errors.New("Referenced Word text part missing")
				}
				related[r.Target] = true
			}
		}
	}
	names := []string{}
	for name := range related {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = paragraphs(p.Parts[name], name, out, &total); err != nil {
			return err
		}
	}
	return nil
}
func paragraphs(root *node, part string, out *Extraction, total *int) error {
	tableIndex := map[*node]int{}
	tables := 0
	paragraphs := 0
	var result error
	walk(root, func(n *node) {
		if wordTag(n.Name, "tbl") {
			tables++
			tableIndex[n] = tables
		}
	})
	walk(root, func(p *node) {
		if result != nil {
			return
		}
		if wordTag(p.Name, "altChunk") || wordTag(p.Name, "subDoc") || (p.Name.Space == "http://schemas.openxmlformats.org/markup-compatibility/2006" && p.Name.Local == "AlternateContent") {
			out.note("external_or_alternate_content_omitted")
		}
		if wordTag(p.Name, "drawing") || wordTag(p.Name, "pict") || wordTag(p.Name, "object") {
			out.note("visual_or_embedded_content_omitted")
		}
		if wordTag(p.Name, "fldSimple") || wordTag(p.Name, "fldChar") {
			out.note("fields_not_evaluated")
		}
		if wordTag(p.Name, "numPr") {
			out.note("generated_numbering_not_rendered")
		}
		if !wordTag(p.Name, "p") {
			return
		}
		paragraphs++
		for n := p; n != nil; n = n.Parent {
			if wordTag(n.Name, "del") || wordTag(n.Name, "moveFrom") {
				return
			}
		}
		var text strings.Builder
		walk(p, func(n *node) {
			if n == p {
				return
			}
			for a := n.Parent; a != nil && a != p; a = a.Parent {
				if wordTag(a.Name, "p") || wordTag(a.Name, "del") || wordTag(a.Name, "moveFrom") {
					return
				}
			}
			switch {
			case wordTag(n.Name, "t"):
				text.WriteString(n.Text.String())
			case wordTag(n.Name, "tab"):
				text.WriteByte('\t')
			case wordTag(n.Name, "br") || wordTag(n.Name, "cr"):
				text.WriteByte('\n')
			}
		})
		locator := part
		var table, row, cell *node
		for n := p.Parent; n != nil; n = n.Parent {
			if cell == nil && wordTag(n.Name, "tc") {
				cell = n
			}
			if row == nil && wordTag(n.Name, "tr") {
				row = n
			}
			if table == nil && wordTag(n.Name, "tbl") {
				table = n
			}
		}
		ordinal := func(n *node, tag string) int {
			if n == nil || n.Parent == nil {
				return 0
			}
			i := 0
			for _, v := range n.Parent.Children {
				if wordTag(v.Name, tag) {
					i++
				}
				if v == n {
					break
				}
			}
			return i
		}
		if table != nil && row != nil && cell != nil {
			locator = fmt.Sprintf("%s/table:%d/row:%d/cell:%d", part, tableIndex[table], ordinal(row, "tr"), ordinal(cell, "tc"))
		}
		result = out.push(fmt.Sprintf("%s/paragraph:%d", locator, paragraphs), text.String(), total)
	})
	return result
}
