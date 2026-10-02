// Package workspace implements the local project authority. Restricted Agents use
// its authenticated HTTP surface, never the database or operator commands.
package workspace

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gobwas/glob"
)

type M = map[string]any

type Error struct {
	Status  int
	Message string
	Applied bool
}

func (e *Error) Error() string           { return e.Message }
func fail(status int, text string) error { return &Error{Status: status, Message: text} }
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 500
}
func Hash(body []byte) string { h := sha256.Sum256(body); return hex.EncodeToString(h[:]) }
func JSON(value any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(value); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
func digest(v any) string { return Hash(JSON(v)) }
func randomID() string    { return Hash([]byte(rand.Text())) }
func now() string         { return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z07:00") }
func Timestamp(s string) (string, error) {
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		return "", fail(422, "Timestamp must be RFC3339 with a timezone")
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000Z07:00"), nil
}
func obj(v any) M {
	m, _ := v.(map[string]any)
	if m == nil {
		return M{}
	}
	return m
}
func str(v any) string { s, _ := v.(string); return s }
func flag(v any) bool  { b, _ := v.(bool); return b }
func list(v any) []any {
	switch a := v.(type) {
	case []any:
		return a
	case []string:
		r := []any{}
		for _, s := range a {
			r = append(r, s)
		}
		return r
	case []M:
		r := []any{}
		for _, s := range a {
			r = append(r, s)
		}
		return r
	}
	return []any{}
}
func strs(v any) []string {
	r := []string{}
	for _, s := range list(v) {
		r = append(r, str(s))
	}
	return r
}
func integer(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case json.Number:
		i, _ := strconv.Atoi(string(n))
		return i
	}
	return 0
}
func keys(m M) []string {
	r := make([]string, 0, len(m))
	for k := range m {
		r = append(r, k)
	}
	slices.Sort(r)
	return r
}
func equal(a, b any) bool { return bytes.Equal(JSON(a), JSON(b)) }
func clone(m M) M {
	v, e := StrictJSON(JSON(m))
	if e != nil {
		panic(e)
	}
	return obj(v)
}
func contains(v any, s string) bool { return slices.Contains(strs(v), s) }
func unique(a []string) []string    { slices.Sort(a); return slices.Compact(a) }
func bounded(s string, min, max int, name string) error {
	n := utf8.RuneCountInString(s)
	if !utf8.ValidString(s) || n < min || n > max || (min > 0 && strings.TrimSpace(s) == "") {
		return fail(422, "Invalid "+name+" length")
	}
	return nil
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ID(s string) error {
	if !idRE.MatchString(s) {
		return fail(422, "Invalid ID")
	}
	return nil
}
func Exact(s string) error {
	if !hashRE.MatchString(s) {
		return fail(422, "A concrete 64-character version ID is required")
	}
	return nil
}

// StrictJSON preserves numeric lexemes, rejects duplicate keys and bounds nesting.
func StrictJSON(b []byte) (any, error) {
	if !utf8.Valid(b) {
		return nil, fail(422, "JSON must be UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var parse func(int) (any, error)
	parse = func(depth int) (any, error) {
		if depth > 128 {
			return nil, fail(422, "JSON nesting exceeds 128")
		}
		t, e := d.Token()
		if e != nil {
			return nil, fail(422, e.Error())
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				m := M{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return nil, fail(422, e.Error())
					}
					key, ok := k.(string)
					if !ok {
						return nil, fail(422, "Invalid object key")
					}
					if _, ok = m[key]; ok {
						return nil, fail(422, "Duplicate JSON key")
					}
					v, e := parse(depth + 1)
					if e != nil {
						return nil, e
					}
					m[key] = v
				}
				t, e = d.Token()
				if e != nil || t != json.Delim('}') {
					return nil, fail(422, "Invalid JSON object")
				}
				return m, nil
			case '[':
				a := []any{}
				for d.More() {
					v, e := parse(depth + 1)
					if e != nil {
						return nil, e
					}
					a = append(a, v)
				}
				t, e = d.Token()
				if e != nil || t != json.Delim(']') {
					return nil, fail(422, "Invalid JSON array")
				}
				return a, nil
			default:
				return nil, fail(422, "Invalid JSON delimiter")
			}
		}
		if n, ok := t.(json.Number); ok {
			f, e := strconv.ParseFloat(string(n), 64)
			if e != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, fail(422, "JSON numeric magnitude exceeds the finite interoperability range")
			}
		}
		return t, nil
	}
	v, e := parse(0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, fail(422, "Trailing JSON data")
	}
	return v, nil
}
func Decode(b []byte, v any) error {
	if _, e := StrictJSON(b); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fail(422, e.Error())
	}
	return nil
}
func normalize(m, defaults M) (M, error) {
	m = clone(m)
	r := clone(defaults)
	for k, v := range m {
		base, ok := defaults[k]
		if !ok {
			return nil, fail(422, "Unknown field: "+k)
		}
		valid := true
		switch base.(type) {
		case string:
			_, valid = v.(string)
		case bool:
			_, valid = v.(bool)
		case int, json.Number:
			_, valid = v.(json.Number)
			if _, ok := v.(int); ok {
				valid = true
			}
			if valid {
				_, e := strconv.Atoi(fmt.Sprint(v))
				valid = e == nil
			}
		case []any, []string, []M:
			switch v.(type) {
			case []any, []string, []M:
			default:
				valid = false
			}
		case map[string]any:
			_, valid = v.(map[string]any)
		}
		if !valid {
			return nil, fail(422, "Invalid type for "+k)
		}
		r[k] = v
	}
	return r, nil
}

type Actor struct {
	ID                string   `json:"id"`
	Roles             []string `json:"roles"`
	Audience          string   `json:"audience"`
	WorkspaceProjects []string `json:"workspace_projects"`
}
type Tokens map[string]Actor

func (a Actor) Validate() error {
	if e := ID(a.ID); e != nil {
		return e
	}
	if len(a.Roles) == 0 || len(a.WorkspaceProjects) > 100 {
		return fail(422, "Invalid actor grants")
	}
	if a.Audience != "operator" && a.Audience != "agent" {
		return fail(422, "Invalid audience")
	}
	for _, r := range a.Roles {
		if !slices.Contains([]string{"reader", "editor", "reviewer", "publisher"}, r) {
			return fail(422, "Invalid role")
		}
		if a.Audience == "agent" && (r == "reviewer" || r == "publisher") {
			return fail(403, "Agents cannot approve or publish")
		}
	}
	for _, p := range a.WorkspaceProjects {
		if e := ID(p); e != nil {
			return e
		}
	}
	return nil
}
func (a Actor) Require(role string) error {
	if e := a.Validate(); e != nil {
		return e
	}
	if !slices.Contains(a.Roles, role) {
		return fail(403, "Role required: "+role)
	}
	return nil
}
func (a Actor) Access(project string) error {
	if e := a.Validate(); e != nil {
		return e
	}
	if e := ID(project); e != nil {
		return e
	}
	if a.Audience == "agent" && !slices.Contains(a.WorkspaceProjects, project) {
		return fail(403, "This token has no workspace grant")
	}
	return nil
}
func (a Actor) Operator(role string) error {
	if e := a.Require(role); e != nil {
		return e
	}
	if a.Audience != "operator" {
		return fail(403, "Operator identity required")
	}
	return nil
}
func ValidateTokens(t Tokens) error {
	if len(t) == 0 {
		return fail(422, "Empty token file")
	}
	seen := map[string]Actor{}
	for k, a := range t {
		if len(k) < 32 || len(k) > 256 {
			return fail(422, "Invalid credential")
		}
		for _, c := range k {
			if c < 33 || c > 126 {
				return fail(422, "Invalid credential")
			}
		}
		if e := a.Validate(); e != nil {
			return e
		}
		if old, ok := seen[a.ID]; ok && !equal(old, a) {
			return fail(422, "Actor identities must be consistent")
		}
		seen[a.ID] = a
	}
	return nil
}

func Pointer(s string) error {
	if e := bounded(s, 0, 500, "JSON pointer"); e != nil {
		return e
	}
	if s != "" && !strings.HasPrefix(s, "/") {
		return fail(422, "Use an RFC6901 pointer")
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' {
			i++
			if i == len(s) || (s[i] != '0' && s[i] != '1') {
				return fail(422, "Invalid JSON pointer escape")
			}
		}
	}
	return nil
}
func at(v any, p string) (any, bool) {
	if Pointer(p) != nil {
		return nil, false
	}
	if p == "" {
		return v, true
	}
	for _, k := range strings.Split(p[1:], "/") {
		k = strings.ReplaceAll(strings.ReplaceAll(k, "~1", "/"), "~0", "~")
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = x[k]
			if !ok {
				return nil, false
			}
		case []any:
			i, e := strconv.Atoi(k)
			if e != nil || i < 0 || i >= len(x) || strconv.Itoa(i) != k {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}
func number(v any) *big.Rat {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = string(n)
	case string:
		s = strings.TrimSpace(n)
	case int:
		s = strconv.Itoa(n)
	default:
		return nil
	}
	if len(s) > 128 || s == "" {
		return nil
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		n, e := strconv.Atoi(s[i+1:])
		if e != nil || n > 1024 || n < -1024 {
			return nil
		}
	}
	if !regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`).MatchString(s) {
		return nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil
	}
	return r
}
func checkOp(v any) bool {
	return slices.Contains([]string{"eq", "gte", "lte", "contains", "exists"}, str(v))
}
func evidence(e M) error {
	if len(e) != 3 {
		return fail(422, "Invalid evidence fields")
	}
	if err := Exact(str(e["source_id"])); err != nil {
		return err
	}
	if err := bounded(str(e["locator"]), 1, 256, "locator"); err != nil {
		return err
	}
	return bounded(str(e["quote"]), 1, 4000, "quote")
}
func Contract(m M) (M, error) {
	c, e := normalize(m, M{"meaning": M{}, "allowed_uses": []any{}, "rows_pointer": "", "sheet": 1, "checks": []any{}})
	if e != nil {
		return nil, e
	}
	if len(list(c["checks"])) > 40 || len(list(c["allowed_uses"])) > 30 || integer(c["sheet"]) < 1 || integer(c["sheet"]) > 100 {
		return nil, fail(422, "Invalid data contract bounds")
	}
	names := []string{"entity_type", "entity_id", "identifier_system", "measure", "unit", "currency", "period", "calendar", "grain", "population", "sampling", "coverage"}
	for k, v := range obj(c["meaning"]) {
		if !slices.Contains(names, k) {
			return nil, fail(422, "Unknown meaning field")
		}
		if e := bounded(str(v), 1, 300, "meaning"); e != nil {
			return nil, e
		}
	}
	for _, v := range list(c["allowed_uses"]) {
		if e := bounded(str(v), 1, 300, "use"); e != nil {
			return nil, e
		}
	}
	if e := Pointer(str(c["rows_pointer"])); e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	rules := []any{}
	for _, v := range list(c["checks"]) {
		r, e := normalize(obj(v), M{"id": "", "op": "", "column": nil, "minimum": nil, "maximum": nil, "max_change": nil})
		if e != nil {
			return nil, e
		}
		name := str(r["id"])
		op := str(r["op"])
		if ID(name) != nil || seen[name] || !slices.Contains([]string{"not_null", "unique", "range", "row_count", "distinct_count_change"}, op) || (op != "row_count") != (r["column"] != nil) {
			return nil, fail(422, "Invalid quality rule")
		}
		seen[name] = true
		if r["column"] != nil {
			if e := bounded(str(r["column"]), 1, 300, "column"); e != nil {
				return nil, e
			}
		}
		for _, k := range []string{"minimum", "maximum", "max_change"} {
			if r[k] != nil {
				if _, ok := r[k].(json.Number); !ok {
					if _, ok = r[k].(int); !ok {
						return nil, fail(422, "Numeric bound required")
					}
				}
				if number(r[k]) == nil {
					return nil, fail(422, "Numeric bound exceeds supported precision/magnitude")
				}
			}
		}
		min, max, delta := number(r["minimum"]), number(r["maximum"]), number(r["max_change"])
		if min != nil && max != nil && min.Cmp(max) > 0 || delta != nil && delta.Sign() < 0 {
			return nil, fail(422, "Invalid numeric interval")
		}
		if op == "range" || op == "row_count" {
			if min == nil && max == nil {
				return nil, fail(422, "Supply a bound")
			}
		} else if min != nil || max != nil {
			return nil, fail(422, "Bounds apply only to range/count")
		}
		if (op == "distinct_count_change") != (delta != nil) {
			return nil, fail(422, "Count change requires max_change")
		}
		rules = append(rules, r)
	}
	c["checks"] = rules
	return c, nil
}
func Metadata(m M) (M, error) {
	r, e := normalize(m, M{"summary": "", "tags": []any{}, "entities": []any{}, "depends_on": []any{}, "facts": M{}, "evidence": []any{}, "owner": "", "authority": 100, "valid_from": nil, "valid_until": nil, "data": nil, "processing": "source", "lineage": []any{}})
	if e != nil {
		return nil, e
	}
	if bounded(str(r["summary"]), 0, 2000, "summary") != nil || bounded(str(r["owner"]), 0, 120, "owner") != nil || len(list(r["tags"])) > 30 || len(list(r["entities"])) > 30 || len(list(r["depends_on"])) > 100 || len(list(r["evidence"])) > 30 || len(list(r["lineage"])) > 100 || len(obj(r["facts"])) > 40 || integer(r["authority"]) < 0 || integer(r["authority"]) > 1000 || len(JSON(r)) > 64000 || len(JSON(r["facts"])) > 16000 {
		return nil, fail(422, "Metadata exceeds limits")
	}
	for k := range obj(r["facts"]) {
		if e := bounded(k, 1, 100, "fact key"); e != nil {
			return nil, e
		}
	}
	for _, v := range list(r["tags"]) {
		if e := bounded(str(v), 1, 300, "tag"); e != nil {
			return nil, e
		}
	}
	for _, v := range list(r["entities"]) {
		if e := ID(str(v)); e != nil {
			return nil, e
		}
	}
	for _, v := range list(r["depends_on"]) {
		if e := Relative(str(v)); e != nil {
			return nil, e
		}
	}
	for _, v := range list(r["evidence"]) {
		if e := evidence(obj(v)); e != nil {
			return nil, e
		}
	}
	for _, v := range list(r["lineage"]) {
		i := obj(v)
		if len(i) != 3 || Exact(str(i["version"])) != nil || Relative(str(i["path"])) != nil || !slices.Contains([]string{"data", "mapping", "code", "model"}, str(i["role"])) {
			return nil, fail(422, "Invalid declared input")
		}
	}
	for _, k := range []string{"valid_from", "valid_until"} {
		if r[k] != nil {
			t, e := Timestamp(str(r[k]))
			if e != nil {
				return nil, e
			}
			r[k] = t
		}
	}
	if r["valid_from"] != nil && r["valid_until"] != nil && str(r["valid_from"]) >= str(r["valid_until"]) {
		return nil, fail(422, "Invalid validity interval")
	}
	if !slices.Contains([]string{"source", "deterministic", "model"}, str(r["processing"])) {
		return nil, fail(422, "Invalid processing kind")
	}
	if r["data"] != nil {
		if _, ok := r["data"].(map[string]any); !ok {
			return nil, fail(422, "Invalid data contract")
		}
		r["data"], e = Contract(obj(r["data"]))
		if e != nil {
			return nil, e
		}
	}
	return r, nil
}
func Spec(m M) (M, error) {
	s, e := normalize(m, M{"id": "", "name": "", "dependencies": M{}, "checks": []any{}, "excludes": []any{}, "includes": []any{"**"}, "data_contracts": M{}})
	if e != nil {
		return nil, e
	}
	if ID(str(s["id"])) != nil || bounded(str(s["name"]), 1, 120, "project name") != nil {
		return nil, fail(422, "Invalid project identity")
	}
	if len(list(s["includes"])) == 0 || len(list(s["includes"])) > 50 || len(list(s["excludes"])) > 50 || len(obj(s["dependencies"])) > 1000 || len(list(s["checks"])) > 40 || len(obj(s["data_contracts"])) > 1000 {
		return nil, fail(422, "Project limits exceeded")
	}
	for _, k := range []string{"includes", "excludes"} {
		for _, p := range list(s[k]) {
			if bounded(str(p), 1, 240, "pattern") != nil {
				return nil, fail(422, "Invalid pattern")
			}
			if _, e := glob.Compile(str(p)); e != nil {
				return nil, fail(422, e.Error())
			}
		}
	}
	for p, ds := range obj(s["dependencies"]) {
		if e := Relative(p); e != nil {
			return nil, e
		}
		if _, ok := ds.([]any); !ok {
			return nil, fail(422, "Dependencies must be arrays")
		}
		if len(list(ds)) > 1000 {
			return nil, fail(422, "Too many dependencies")
		}
		for _, d := range list(ds) {
			if e := Relative(str(d)); e != nil {
				return nil, e
			}
		}
	}
	seen := map[string]bool{}
	checks := []any{}
	for _, v := range list(s["checks"]) {
		c, e := normalize(obj(v), M{"id": "", "file": "", "pointer": "", "op": "eq", "expected": nil, "reference_file": nil, "reference_pointer": ""})
		if e != nil {
			return nil, e
		}
		id := str(c["id"])
		if ID(id) != nil || seen[id] || Relative(str(c["file"])) != nil || Pointer(str(c["pointer"])) != nil || Pointer(str(c["reference_pointer"])) != nil || !checkOp(c["op"]) {
			return nil, fail(422, "Invalid regression check")
		}
		seen[id] = true
		if c["reference_file"] != nil {
			if e := Relative(str(c["reference_file"])); e != nil {
				return nil, e
			}
		}
		checks = append(checks, c)
	}
	s["checks"] = checks
	for p, c := range obj(s["data_contracts"]) {
		if e := Relative(p); e != nil {
			return nil, e
		}
		contract, e := Contract(obj(c))
		if e != nil {
			return nil, e
		}
		obj(s["data_contracts"])[p] = contract
	}
	return s, nil
}
func Query(m M) (M, error) {
	q, e := normalize(m, M{"version": "latest", "paths": []any{}, "as_of": nil, "path": nil, "include_bytes": false, "query": nil, "mode": "hybrid", "limit": 30, "tags": []any{}, "max_per_file": 3, "before": nil, "after": "latest", "base_version": nil, "direction": "forward", "goal": nil, "max_chars": 12000, "output_checks": []any{}, "retrieval_mode": "hybrid", "retrieval_limit": 5, "requirements": M{}, "model_use": "none", "phase": "build", "task_id": nil, "operation": "read", "output_version": nil, "outputs": M{}, "result": M{}, "citations": []any{}})
	if e != nil {
		return nil, e
	}
	if bounded(str(q["version"]), 1, 128, "version") != nil || len(list(q["paths"])) > 1000 || len(obj(q["requirements"])) > 1000 || len(obj(q["outputs"])) > 1000 || len(list(q["citations"])) > 100 || len(list(q["output_checks"])) > 40 || len(list(q["tags"])) > 30 {
		return nil, fail(422, "Query limits exceeded")
	}
	for k, bounds := range map[string][2]int{"limit": {1, 100}, "max_per_file": {1, 100}, "max_chars": {100, 50000}, "retrieval_limit": {1, 20}} {
		n := integer(q[k])
		if n < bounds[0] || n > bounds[1] {
			return nil, fail(422, "Query limits exceeded")
		}
	}
	paths := append(strs(q["paths"]), keys(obj(q["requirements"]))...)
	paths = append(paths, keys(obj(q["outputs"]))...)
	if q["path"] != nil {
		paths = append(paths, str(q["path"]))
	}
	for _, p := range paths {
		if e := Relative(p); e != nil {
			return nil, e
		}
	}
	if q["as_of"] != nil {
		t, e := Timestamp(str(q["as_of"]))
		if e != nil {
			return nil, e
		}
		if t > now() {
			return nil, fail(422, "as_of cannot be in the future")
		}
		q["as_of"] = t
	}
	for _, k := range []string{"query", "goal"} {
		if q[k] != nil {
			max := 2000
			if k == "query" {
				max = 200
			}
			if e := bounded(str(q[k]), 1, max, k); e != nil {
				return nil, e
			}
		}
	}
	for k, values := range map[string][]string{"mode": {"exact", "lexical", "hybrid", "semantic"}, "retrieval_mode": {"exact", "lexical", "hybrid", "semantic"}, "direction": {"forward", "reverse"}, "model_use": {"none", "extractive", "generative"}, "phase": {"build", "preflight", "postflight"}, "operation": {"read", "search", "diff", "impact", "compile", "write", "publish"}} {
		if !slices.Contains(values, str(q[k])) {
			return nil, fail(422, "Invalid query mode")
		}
	}
	for p, v := range obj(q["requirements"]) {
		u, e := normalize(obj(v), M{"expect": M{}, "purpose": nil, "require_quality": true})
		if e != nil {
			return nil, e
		}
		if _, e = Contract(M{"meaning": u["expect"]}); e != nil {
			return nil, e
		}
		if u["purpose"] != nil {
			if e := bounded(str(u["purpose"]), 1, 300, "purpose"); e != nil {
				return nil, e
			}
		}
		obj(q["requirements"])[p] = u
	}
	for _, v := range list(q["tags"]) {
		if e := bounded(str(v), 1, 300, "tag"); e != nil {
			return nil, e
		}
	}
	for _, v := range obj(q["outputs"]) {
		if e := Exact(str(v)); e != nil {
			return nil, e
		}
	}
	for _, v := range list(q["citations"]) {
		if e := evidence(obj(v)); e != nil {
			return nil, e
		}
	}
	checks := []any{}
	seen := map[string]bool{}
	for _, v := range list(q["output_checks"]) {
		c, e := normalize(obj(v), M{"id": "", "object_id": "", "field": "", "op": "eq", "expected": nil})
		if e != nil {
			return nil, e
		}
		id := str(c["id"])
		if ID(id) != nil || seen[id] || c["object_id"] != "result" || bounded(str(c["field"]), 1, 128, "result field") != nil || !checkOp(c["op"]) {
			return nil, fail(422, "Invalid output check")
		}
		seen[id] = true
		checks = append(checks, c)
	}
	q["output_checks"] = checks
	return q, nil
}
func WriteQuery(m M) (M, error) {
	q, e := normalize(m, M{"base_version": "", "request_id": "", "changes": M{}, "message": "", "task": "", "model": "", "tool": "", "dry_run": false, "require_pass": false})
	if e != nil {
		return nil, e
	}
	if Exact(str(q["base_version"])) != nil || ID(str(q["request_id"])) != nil || len(obj(q["changes"])) < 1 || len(obj(q["changes"])) > 1000 {
		return nil, fail(422, "Invalid write identity or changes")
	}
	for k, max := range map[string]int{"message": 500, "task": 500, "model": 200, "tool": 200} {
		min := 0
		if k == "message" {
			min = 1
		}
		if e := bounded(str(q[k]), min, max, k); e != nil {
			return nil, e
		}
	}
	for p, v := range obj(q["changes"]) {
		if e := Relative(p); e != nil {
			return nil, e
		}
		c, e := normalize(obj(v), M{"text": nil, "base64": nil, "delete": false, "meta": nil})
		if e != nil {
			return nil, e
		}
		n := 0
		for _, k := range []string{"text", "base64"} {
			if c[k] != nil {
				if _, ok := c[k].(string); !ok {
					return nil, fail(422, "Content must be a string")
				}
				n++
			}
		}
		if flag(c["delete"]) {
			n++
		}
		if n > 1 || n == 0 && c["meta"] == nil || flag(c["delete"]) && c["meta"] != nil {
			return nil, fail(422, "Choose text/base64/delete or metadata-only")
		}
		if c["meta"] != nil {
			if _, ok := c["meta"].(map[string]any); !ok {
				return nil, fail(422, "Metadata must be an object")
			}
			c["meta"], e = Metadata(obj(c["meta"]))
			if e != nil {
				return nil, e
			}
		}
		obj(q["changes"])[p] = c
	}
	return q, nil
}

type Version struct {
	ID          string  `json:"id"`
	Snapshot    M       `json:"snapshot"`
	Status      string  `json:"status"`
	AvailableAt *string `json:"available_at"`
	Approver    *string `json:"approver"`
	Revoked     bool    `json:"revoked"`
}

func (v *Version) Files() M           { return obj(v.Snapshot["files"]) }
func (v *Version) File(path string) M { return obj(v.Files()[path]) }
func temporal(v *Version, asof any) M {
	var availability any
	if v.AvailableAt != nil {
		availability = M{"available_at": *v.AvailableAt, "version": v.ID, "basis": "filewise_completed_snapshot"}
	}
	return M{"as_of": asof, "availability": availability, "scope": "completed_project_snapshot; not row-level point-in-time joins"}
}
