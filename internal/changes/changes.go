// Package changes records the meaning supported by extracted evidence. Exact
// quantity changes are rule-derived; arbitrary prose changes require review.
package changes

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/huaiwen/filewise/internal/documents"
)

type Source struct {
	SHA256     string         `json:"sha256"`
	Extraction documents.Info `json:"extraction"`
}
type Evidence struct {
	SHA256    string `json:"sha256"`
	Locator   string `json:"locator"`
	Quote     string `json:"quote"`
	Truncated bool   `json:"truncated,omitempty"`
}
type Quantity struct {
	Subject        string `json:"subject"`
	Before         string `json:"before"`
	After          string `json:"after"`
	Unit           string `json:"unit"`
	Delta          string `json:"delta"`
	RelativeChange string `json:"relative_change,omitempty"` // Exact rational; absent for a zero baseline.
}
type Change struct {
	Kind           string    `json:"kind"`
	Meaning        string    `json:"meaning"`
	Status         string    `json:"status"`
	RequiresReview bool      `json:"requires_review"`
	Before         *Evidence `json:"before,omitempty"`
	After          *Evidence `json:"after,omitempty"`
	Quantity       *Quantity `json:"quantity,omitempty"`
}
type Metadata struct {
	Schema         string   `json:"schema"`
	Engine         string   `json:"engine"`
	Before         *Source  `json:"before,omitempty"`
	After          Source   `json:"after"`
	Summary        string   `json:"summary"`
	Status         string   `json:"status"`
	Coverage       string   `json:"coverage"`
	ImpactStatus   string   `json:"impact_status"`
	RequiresReview bool     `json:"requires_review"`
	Changes        []Change `json:"changes"`
	Notes          []string `json:"notes"`
}

func source(body []byte, e documents.Extraction) Source { return Source{documents.Hash(body), e.Info} }
func evidence(s Source, f documents.Fragment) *Evidence {
	text := []rune(f.Text)
	truncated := len(text) > 4000
	if truncated {
		text = text[:4000]
	}
	return &Evidence{s.SHA256, f.Locator, string(text), truncated}
}

// Derive does not alter user-authored metadata, infer downstream impact, call a
// model, or treat extracted text as instructions. It is deterministic for inputs.
func Derive(name string, before, after []byte, hasBefore bool) Metadata {
	b := documents.Extract(name, after)
	m := Metadata{Schema: "filewise-go/change-v1", Engine: "filewise-go-semantic-rules-v1", After: source(after, b), Status: "recorded", Coverage: "extracted_text_only", ImpactStatus: "not_evaluated", Changes: []Change{}, Notes: []string{}}
	incomplete := func(e documents.Extraction) bool { return e.Info.Status != "text" }
	if !hasBefore {
		m.Summary = "首次记录文件；没有前一版本可供比较。"
		m.Changes = append(m.Changes, Change{Kind: "created", Meaning: m.Summary, Status: "observed"})
		m.RequiresReview = incomplete(b)
		if m.RequiresReview {
			m.Status = "incomplete"
		}
		return m
	}
	a := documents.Extract(name, before)
	old := source(before, a)
	m.Before = &old
	if incomplete(a) || incomplete(b) {
		m.RequiresReview = true
		m.Status = "incomplete"
		m.Notes = append(m.Notes, "extraction_incomplete; no claim of complete document change coverage")
	}
	if old.SHA256 == m.After.SHA256 {
		m.Summary = "文件内容未变化。"
		return m
	}
	// ponytail: bounded LCS for paragraph alignment; above one million cells,
	// retain the source hashes and require review rather than guessing matches.
	n, k := len(a.Fragments), len(b.Fragments)
	if (n+1)*(k+1) > 1_000_000 {
		m.RequiresReview = true
		m.Status = "needs_review"
		m.Summary = "文件已变化；段落对齐超过计算上限，需复核。"
		m.Notes = append(m.Notes, "alignment_budget_exceeded")
		return m
	}
	same := func(x, y documents.Fragment) bool { return scope(x.Locator) == scope(y.Locator) && x.Text == y.Text }
	stride := k + 1
	dp := make([]uint16, (n+1)*stride)
	for i := n - 1; i >= 0; i-- {
		for j := k - 1; j >= 0; j-- {
			if same(a.Fragments[i], b.Fragments[j]) {
				dp[i*stride+j] = 1 + dp[(i+1)*stride+j+1]
			} else {
				v := dp[(i+1)*stride+j]
				if dp[i*stride+j+1] > v {
					v = dp[i*stride+j+1]
				}
				dp[i*stride+j] = v
			}
		}
	}
	// Repeated paragraphs can admit multiple equally valid alignments. Preserve
	// quoted deltas, but do not turn a guessed pairing into a semantic assertion.
	ambiguous := duplicates(a.Fragments) || duplicates(b.Fragments)
	emit := func(x, y []documents.Fragment) {
		if len(m.Changes) >= 200 {
			m.RequiresReview = true
			m.Status = "needs_review"
			return
		}
		if len(x) == 1 && len(y) == 1 && scope(x[0].Locator) == scope(y[0].Locator) {
			change := Change{Kind: "modified", Meaning: "段落表述发生变化；需核对其含义。", Status: "needs_review", RequiresReview: true, Before: evidence(old, x[0]), After: evidence(m.After, y[0])}
			if !ambiguous {
				if q, ok := quantityChange(x[0].Text, y[0].Text); ok {
					change.Quantity = q
					change.Status = "rule_derived"
					change.RequiresReview = false
					change.Meaning = fmt.Sprintf("%s：%s %s → %s %s（变动 %s %s）。", q.Subject, q.Before, q.Unit, q.After, q.Unit, q.Delta, q.Unit)
				}
			} else {
				change.Status = "ambiguous_alignment"
			}
			m.Changes = append(m.Changes, change)
			return
		}
		for _, f := range x {
			if len(m.Changes) >= 200 {
				m.RequiresReview = true
				m.Status = "needs_review"
				break
			}
			m.Changes = append(m.Changes, Change{Kind: "removed", Meaning: "删除段落；影响需复核。", Status: "needs_review", RequiresReview: true, Before: evidence(old, f)})
		}
		for _, f := range y {
			if len(m.Changes) >= 200 {
				m.RequiresReview = true
				m.Status = "needs_review"
				break
			}
			m.Changes = append(m.Changes, Change{Kind: "added", Meaning: "新增段落；影响需复核。", Status: "needs_review", RequiresReview: true, After: evidence(m.After, f)})
		}
	}
	i, j, ai, bj := 0, 0, 0, 0
	for i < n && j < k {
		if same(a.Fragments[i], b.Fragments[j]) {
			emit(a.Fragments[ai:i], b.Fragments[bj:j])
			i++
			j++
			ai = i
			bj = j
			continue
		}
		if dp[(i+1)*stride+j] >= dp[i*stride+j+1] {
			i++
		} else {
			j++
		}
	}
	emit(a.Fragments[ai:], b.Fragments[bj:])
	if len(m.Changes) >= 200 {
		m.Notes = append(m.Notes, "change_detail_limit; review the complete source versions")
	}
	truncated := false
	for i := range m.Changes {
		c := &m.Changes[i]
		if (c.Before != nil && c.Before.Truncated) || (c.After != nil && c.After.Truncated) {
			c.RequiresReview = true
			truncated = true
		}
		m.RequiresReview = m.RequiresReview || c.RequiresReview
	}
	if truncated {
		m.Notes = append(m.Notes, "quoted_evidence_truncated; read full source versions")
	}
	if m.RequiresReview && m.Status == "recorded" {
		m.Status = "needs_review"
	}
	switch len(m.Changes) {
	case 0:
		m.Summary = "原文件字节已变化；已提取文本未变化。"
		if incomplete(a) || incomplete(b) {
			m.Summary = "文件已变化；文本提取不完整，无法判断全部变动语义。"
		}
		m.Notes = append(m.Notes, "package_or_nontext_changes_not_interpreted")
		m.RequiresReview = true
		if m.Status == "recorded" {
			m.Status = "needs_review"
		}
	case 1:
		m.Summary = m.Changes[0].Meaning
	default:
		m.Summary = fmt.Sprintf("记录了 %d 项有原文依据的变化。", len(m.Changes))
	}
	return m
}
func scope(locator string) string {
	if i := strings.LastIndex(locator, "/paragraph:"); i >= 0 {
		return locator[:i]
	}
	if strings.HasPrefix(locator, "line:") {
		return "text"
	}
	return locator
}
func duplicates(f []documents.Fragment) bool {
	seen := map[string]bool{}
	for _, v := range f {
		key := scope(v.Locator) + "\x00" + v.Text
		if seen[key] {
			return true
		}
		seen[key] = true
	}
	return false
}

var quantityRE = regexp.MustCompile(`([+-]?[0-9]+(?:\.[0-9]+)?)[ \t]*(kPa|MPa|Pa|mm|cm|kg|ms|%|℃|°C|万元|元|天|小时|分钟|人)([^A-Za-z]|$)`)
var otherNumber = regexp.MustCompile(`[0-9]`)

func quantityChange(before, after string) (*Quantity, bool) {
	parse := func(text string) ([]string, bool) {
		matches := quantityRE.FindAllStringSubmatchIndex(text, 2)
		if len(matches) != 1 {
			return nil, false
		}
		m := matches[0]
		prefix, suffix := text[:m[2]], text[m[5]:]
		if otherNumber.MatchString(prefix+suffix) || strings.TrimSpace(prefix) == "" || len(prefix) > 400 || m[3]-m[2] > 64 {
			return nil, false
		}
		return []string{prefix, text[m[2]:m[3]], text[m[4]:m[5]], suffix}, true
	}
	a, ok := parse(before)
	if !ok {
		return nil, false
	}
	b, ok := parse(after)
	if !ok || a[0] != b[0] || a[2] != b[2] || a[3] != b[3] {
		return nil, false
	}
	x, ok := new(big.Rat).SetString(a[1])
	if !ok {
		return nil, false
	}
	y, ok := new(big.Rat).SetString(b[1])
	if !ok || x.Cmp(y) == 0 {
		return nil, false
	}
	delta := new(big.Rat).Sub(y, x)
	digits := func(s string) int {
		if i := strings.IndexByte(s, '.'); i >= 0 {
			return len(s) - i - 1
		}
		return 0
	}
	scale := max(digits(a[1]), digits(b[1]))
	decimal := delta.FloatString(scale)
	if strings.Contains(decimal, ".") {
		decimal = strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
	}
	if delta.Sign() > 0 {
		decimal = "+" + decimal
	}
	q := &Quantity{Subject: strings.TrimSpace(a[0]), Before: a[1], After: b[1], Unit: a[2], Delta: decimal}
	if x.Sign() != 0 {
		q.RelativeChange = new(big.Rat).Quo(delta, x).RatString()
	}
	return q, true
}
