package documents

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const slideNS = "http://schemas.openxmlformats.org/presentationml/2006/main"
const drawingNS = "http://schemas.openxmlformats.org/drawingml/2006/main"
const sheetNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

func tag(n *node, ns, local string) bool {
	strict := strings.ReplaceAll(strings.ReplaceAll(ns, "http://schemas.openxmlformats.org/", "http://purl.oclc.org/ooxml/"), "/2006/", "/")
	return n != nil && n.Name.Local == local && (n.Name.Space == ns || n.Name.Space == strict)
}
func descendants(root *node, ns, local string) []*node {
	result := []*node{}
	walk(root, func(n *node) {
		if tag(n, ns, local) {
			result = append(result, n)
		}
	})
	return result
}
func relID(n *node) string {
	for _, a := range n.Attr {
		if a.Name.Local == "id" && (a.Name.Space == strings.TrimSuffix(officeNS, "/") || a.Name.Space == strings.TrimSuffix(strictOfficeNS, "/")) {
			return a.Value
		}
	}
	return ""
}
func (p *officePackage) mainPart() (string, error) {
	rels, e := p.relations("")
	if e != nil {
		return "", e
	}
	main := ""
	for _, r := range rels {
		if relationKind(r.Kind, "officeDocument") {
			if main != "" || r.External {
				return "", errors.New("Invalid Office main relationship")
			}
			main = r.Target
		}
	}
	if main == "" {
		return "", errors.New("Office main relationship missing")
	}
	return main, nil
}
func (p *officePackage) required(rels map[string]relationship, id, kind string) (string, *node, error) {
	r, ok := rels[id]
	if !ok || r.External || !relationKind(r.Kind, kind) {
		return "", nil, errors.New("External or mismatched Office relationship")
	}
	n := p.Parts[r.Target]
	if n == nil {
		return "", nil, errors.New("Referenced Office part missing")
	}
	return r.Target, n, nil
}
func slides(body []byte, out *Extraction) error {
	p, e := openPackage(body)
	if e != nil {
		return e
	}
	for _, note := range p.Notes {
		out.note(note)
	}
	main, e := p.mainPart()
	if e != nil {
		return e
	}
	root := p.Parts[main]
	if !tag(root, slideNS, "presentation") {
		return errors.New("Not a PowerPoint presentation")
	}
	rels, e := p.relations(main)
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	total := 0
	for i, n := range descendants(root, slideNS, "sldId") {
		if i >= 500 {
			return errors.New("Presentation exceeds 500 slides")
		}
		part, slide, e := p.required(rels, relID(n), "slide")
		if e != nil {
			return e
		}
		if seen[part] || !tag(slide, slideNS, "sld") {
			return errors.New("Invalid or repeated slide relationship")
		}
		seen[part] = true
		if e = slideParagraphs(slide, fmt.Sprintf("slide:%d", i+1), out, &total); e != nil {
			return e
		}
		related, e := p.relations(part)
		if e != nil {
			return e
		}
		ids := []string{}
		for id, r := range related {
			if relationKind(r.Kind, "slideLayout") {
				out.note("layout_templates_not_rendered")
			}
			if relationKind(r.Kind, "notesSlide") {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			_, notes, e := p.required(related, id, "notesSlide")
			if e != nil {
				return e
			}
			if !tag(notes, slideNS, "notes") {
				return errors.New("Invalid slide notes root")
			}
			if e = slideParagraphs(notes, fmt.Sprintf("slide:%d/notes", i+1), out, &total); e != nil {
				return e
			}
		}
	}
	return nil
}
func slideParagraphs(root *node, prefix string, out *Extraction, total *int) error {
	tables := map[*node]int{}
	for i, n := range descendants(root, drawingNS, "tbl") {
		tables[n] = i + 1
	}
	ordinal := func(n *node, local string) int {
		if n == nil || n.Parent == nil {
			return 0
		}
		i := 0
		for _, c := range n.Parent.Children {
			if tag(c, drawingNS, local) {
				i++
			}
			if c == n {
				break
			}
		}
		return i
	}
	for i, p := range descendants(root, drawingNS, "p") {
		var text strings.Builder
		walk(p, func(n *node) {
			if n == p {
				return
			}
			for a := n.Parent; a != nil && a != p; a = a.Parent {
				if tag(a, drawingNS, "p") {
					return
				}
			}
			if tag(n, drawingNS, "t") {
				text.WriteString(n.Text.String())
			} else if tag(n, drawingNS, "tab") {
				text.WriteByte('\t')
			} else if tag(n, drawingNS, "br") {
				text.WriteByte('\n')
			}
		})
		loc := prefix
		var table, row, cell *node
		for a := p.Parent; a != nil; a = a.Parent {
			if cell == nil && tag(a, drawingNS, "tc") {
				cell = a
			}
			if row == nil && tag(a, drawingNS, "tr") {
				row = a
			}
			if table == nil && tag(a, drawingNS, "tbl") {
				table = a
			}
		}
		if table != nil && row != nil && cell != nil {
			loc = fmt.Sprintf("%s/table:%d/row:%d/cell:%d", prefix, tables[table], ordinal(row, "tr"), ordinal(cell, "tc"))
		}
		if e := out.push(fmt.Sprintf("%s/paragraph:%d", loc, i+1), text.String(), total); e != nil {
			return e
		}
	}
	return nil
}

type Cell struct {
	Address       string  `json:"address"`
	Row           int     `json:"row"`
	Column        int     `json:"column"`
	Kind          string  `json:"kind"`
	Value         any     `json:"value"`
	Formula       *string `json:"formula"`
	FormulaOrigin *string `json:"formula_origin"`
}
type Sheet struct {
	Name  string `json:"name"`
	Cells []Cell `json:"cells"`
}

var addressRE = regexp.MustCompile(`^([A-Z]{1,3})([1-9][0-9]{0,4})$`)

func address(s string) (int, int, error) {
	m := addressRE.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, errors.New("Invalid cell address")
	}
	row, _ := strconv.Atoi(m[2])
	col := 0
	for _, c := range m[1] {
		col = col*26 + int(c-'A'+1)
	}
	if row > 20001 || col > 512 {
		return 0, 0, errors.New("Sheet exceeds 20,001 rows or 512 columns")
	}
	return row, col, nil
}
func rich(root *node) string {
	var b strings.Builder
	walk(root, func(n *node) {
		if !tag(n, sheetNS, "t") {
			return
		}
		for a := n.Parent; a != nil; a = a.Parent {
			if tag(a, sheetNS, "rPh") {
				return
			}
		}
		b.WriteString(n.Text.String())
	})
	return b.String()
}
func dateFormat(id int, format string) bool {
	if id >= 14 && id <= 22 || id >= 27 && id <= 36 || id >= 45 && id <= 47 || id >= 50 && id <= 58 {
		return true
	}
	quoted, escaped, bracket := false, false, false
	for _, c := range strings.ToLower(format) {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if c == '[' {
			bracket = true
			continue
		}
		if c == ']' {
			bracket = false
			continue
		}
		if (!bracket || strings.ContainsRune("hms", c)) && strings.ContainsRune("ymdhs", c) {
			return true
		}
	}
	return false
}

var numericRE = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`)

func numeric(s string) bool {
	if len(s) > 128 || !numericRE.MatchString(s) {
		return false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		n, e := strconv.Atoi(s[i+1:])
		if e != nil || n < -1024 || n > 1024 {
			return false
		}
	}
	_, ok := new(big.Rat).SetString(s)
	return ok
}
func child(n *node, ns, local string) *node {
	for _, c := range n.Children {
		if tag(c, ns, local) {
			return c
		}
	}
	return nil
}
func workbook(body []byte, out *Extraction) error {
	p, e := openPackage(body)
	if e != nil {
		return e
	}
	for _, note := range p.Notes {
		out.note(note)
	}
	main, e := p.mainPart()
	if e != nil {
		return e
	}
	book := p.Parts[main]
	if !tag(book, sheetNS, "workbook") {
		return errors.New("Not an Excel workbook")
	}
	rels, e := p.relations(main)
	if e != nil {
		return e
	}
	date1904 := false
	for _, n := range descendants(book, sheetNS, "workbookPr") {
		date1904 = attr(n, "date1904") == "1" || attr(n, "date1904") == "true"
	}
	shared := []string{}
	styles := []bool{}
	seenResources := map[string]bool{}
	ids := []string{}
	for id := range rels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := rels[id]
		kind := ""
		if relationKind(r.Kind, "sharedStrings") {
			kind = "sharedStrings"
		} else if relationKind(r.Kind, "styles") {
			kind = "styles"
		}
		if kind == "" {
			continue
		}
		if seenResources[kind] {
			return errors.New("Repeated workbook text resource")
		}
		seenResources[kind] = true
		_, doc, e := p.required(rels, id, kind)
		if e != nil {
			return e
		}
		if kind == "sharedStrings" {
			if !tag(doc, sheetNS, "sst") {
				return errors.New("Invalid shared strings root")
			}
			for _, n := range descendants(doc, sheetNS, "si") {
				if len(shared) >= maxFragments {
					return errors.New("Too many shared strings")
				}
				shared = append(shared, rich(n))
			}
		} else {
			if !tag(doc, sheetNS, "styleSheet") {
				return errors.New("Invalid styles root")
			}
			formats := map[int]string{}
			for _, n := range descendants(doc, sheetNS, "numFmt") {
				id, e := strconv.Atoi(attr(n, "numFmtId"))
				if e != nil || id < 0 || attr(n, "formatCode") == "" {
					return errors.New("Invalid number format")
				}
				if _, ok := formats[id]; ok {
					return errors.New("Repeated number format")
				}
				formats[id] = attr(n, "formatCode")
			}
			for _, n := range descendants(doc, sheetNS, "xf") {
				if !tag(n.Parent, sheetNS, "cellXfs") {
					continue
				}
				value := attr(n, "numFmtId")
				if value == "" {
					value = "0"
				}
				id, e := strconv.Atoi(value)
				if e != nil || id < 0 {
					return errors.New("Invalid style number format")
				}
				if id >= 164 && formats[id] == "" {
					return errors.New("Missing custom number format")
				}
				styles = append(styles, dateFormat(id, formats[id]))
			}
		}
	}
	names, parts := map[string]bool{}, map[string]bool{}
	total := 0
	cellCount := 0
	for index, n := range descendants(book, sheetNS, "sheet") {
		if index >= 100 {
			return errors.New("Too many worksheets")
		}
		name := attr(n, "name")
		if name == "" || utf8.RuneCountInString(name) > 100 || names[name] {
			return errors.New("Invalid or repeated sheet name")
		}
		names[name] = true
		part, doc, e := p.required(rels, relID(n), "worksheet")
		if e != nil {
			return e
		}
		if parts[part] || !tag(doc, sheetNS, "worksheet") {
			return errors.New("Invalid or repeated worksheet root")
		}
		parts[part] = true
		sheet := Sheet{Name: name, Cells: []Cell{}}
		seen := map[string]bool{}
		type formulaRange struct {
			r1, c1, r2, c2 int
			origin         string
		}
		ranges := []formulaRange{}
		for _, f := range descendants(doc, sheetNS, "f") {
			ref := attr(f, "ref")
			if ref == "" {
				continue
			}
			start, end, ok := strings.Cut(ref, ":")
			if !ok {
				end = start
			}
			r1, c1, e := address(start)
			if e != nil {
				return e
			}
			r2, c2, e := address(end)
			if e != nil {
				return e
			}
			if f.Parent == nil {
				return errors.New("Invalid formula parent")
			}
			origin := attr(f.Parent, "r")
			row, col, e := address(origin)
			if e != nil {
				return e
			}
			if r1 > r2 || c1 > c2 || row < r1 || row > r2 || col < c1 || col > c2 || len(ranges) >= 1000 {
				return errors.New("Invalid or excessive formula ranges")
			}
			ranges = append(ranges, formulaRange{r1, c1, r2, c2, origin})
		}
		for _, n := range descendants(doc, sheetNS, "c") {
			if !tag(n.Parent, sheetNS, "row") {
				continue
			}
			ref := attr(n, "r")
			row, col, e := address(ref)
			if e != nil {
				return e
			}
			if v := attr(n.Parent, "r"); v != "" {
				r, e := strconv.Atoi(v)
				if e != nil || r != row {
					return errors.New("Cell address conflicts with its row")
				}
			}
			if seen[ref] {
				return errors.New("Duplicate or excessive cells")
			}
			seen[ref] = true
			cellCount++
			if cellCount > maxFragments {
				return errors.New("Duplicate or excessive cells")
			}
			raw := ""
			if v := child(n, sheetNS, "v"); v != nil {
				raw = v.Text.String()
			}
			var formula, origin *string
			if f := child(n, sheetNS, "f"); f != nil {
				value := f.Text.String()
				formula = &value
			}
			for _, r := range ranges {
				if row >= r.r1 && row <= r.r2 && col >= r.c1 && col <= r.c2 {
					v := r.origin
					origin = &v
					break
				}
			}
			if formula != nil || origin != nil {
				out.note("formulas_not_evaluated")
			}
			styleText := attr(n, "s")
			if styleText == "" {
				styleText = "0"
			}
			style, e := strconv.Atoi(styleText)
			if e != nil || style < 0 || len(styles) > 0 && style >= len(styles) || len(styles) == 0 && style > 0 {
				return errors.New("Missing or invalid cell style")
			}
			kind := ""
			var value any
			switch attr(n, "t") {
			case "s":
				i, e := strconv.Atoi(raw)
				if e != nil || i < 0 || i >= len(shared) {
					return errors.New("Missing or invalid shared string")
				}
				kind, value = "string", shared[i]
			case "inlineStr":
				kind, value = "string", rich(n)
			case "str":
				kind, value = "string", raw
			case "b":
				if raw != "0" && raw != "1" {
					return errors.New("Invalid boolean cell")
				}
				kind, value = "boolean", raw == "1"
			case "e":
				kind, value = "error", raw
			case "d":
				kind, value = "date", map[string]any{"iso_date": raw}
			case "n", "":
				if raw == "" {
					kind = "empty"
				} else {
					if !numeric(raw) {
						return errors.New("Invalid or excessive numeric cell")
					}
					if len(styles) > 0 && styles[style] {
						system := 1900
						if date1904 {
							system = 1904
						}
						kind, value = "date", map[string]any{"excel_serial": raw, "date_system": system}
					} else {
						kind, value = "number", raw
					}
				}
			default:
				return errors.New("Unsupported cell type")
			}
			empty := value == nil
			if text, ok := value.(string); ok && text == "" {
				empty = true
			}
			if formula == nil && origin == nil && empty {
				continue
			}
			display := ""
			if text, ok := value.(string); ok {
				display = text
			} else {
				b, e := json.Marshal(value)
				if e != nil {
					return e
				}
				display = string(b)
			}
			if formula != nil {
				display = "=" + *formula + " [cached: " + display + "]"
			} else if origin != nil {
				display = "[formula cache from " + *origin + ": " + display + "]"
			}
			if e = out.push(fmt.Sprintf("sheet:%d/cell:%s", index+1, ref), display, &total); e != nil {
				return e
			}
			sheet.Cells = append(sheet.Cells, Cell{ref, row, col, kind, value, formula, origin})
		}
		sort.Slice(sheet.Cells, func(i, j int) bool {
			a, b := sheet.Cells[i], sheet.Cells[j]
			return a.Row < b.Row || a.Row == b.Row && a.Column < b.Column
		})
		out.Sheets = append(out.Sheets, sheet)
	}
	return nil
}

// Table never promotes formula caches, dates, booleans or error cells to numbers.
func Table(extraction Extraction, index int) ([]map[string]any, []string, error) {
	if extraction.Info.Status == "failed" {
		return nil, nil, errors.New(extraction.Info.Error)
	}
	if index < 1 || index > len(extraction.Sheets) {
		return nil, nil, errors.New("Worksheet is unavailable")
	}
	sheet := extraction.Sheets[index-1]
	if len(sheet.Cells) == 0 {
		return nil, nil, errors.New("Worksheet has no header")
	}
	first, last := sheet.Cells[0].Row, sheet.Cells[len(sheet.Cells)-1].Row
	width := 0
	headers := map[int]Cell{}
	for _, c := range sheet.Cells {
		width = max(width, c.Column)
		if c.Row == first {
			headers[c.Column] = c
		}
	}
	names := []string{}
	seen := map[string]bool{}
	for i := 1; i <= width; i++ {
		c, ok := headers[i]
		if !ok {
			return nil, nil, errors.New("Worksheet header has empty cells")
		}
		name, ok := c.Value.(string)
		if !ok || strings.TrimSpace(name) == "" || c.Kind != "string" || c.Formula != nil || c.FormulaOrigin != nil || utf8.RuneCountInString(name) > 300 || seen[name] {
			return nil, nil, errors.New("Worksheet headers must be unique literal strings")
		}
		seen[name] = true
		names = append(names, name)
	}
	rows := make([]map[string]any, last-first)
	for i := range rows {
		rows[i] = map[string]any{}
	}
	for _, c := range sheet.Cells {
		if c.Row == first {
			continue
		}
		value := c.Value
		if c.Formula != nil || c.FormulaOrigin != nil || c.Kind == "error" {
			value = nil
		}
		rows[c.Row-first-1][names[c.Column-1]] = value
	}
	return rows, names, nil
}
