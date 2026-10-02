package workspace

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testActor(id, role string) Actor {
	return Actor{ID: id, Roles: []string{role}, Audience: "operator", WorkspaceProjects: []string{}}
}

var editor = testActor("editor", "editor")
var reviewer = testActor("reviewer", "reviewer")
var publisher = testActor("publisher", "publisher")
var agent = Actor{ID: "agent", Roles: []string{"reader", "editor"}, Audience: "agent", WorkspaceProjects: []string{"p"}}

func require(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func assert(t *testing.T, b bool, message string) {
	t.Helper()
	if !b {
		t.Fatal(message)
	}
}
func wantError(t *testing.T, e error, text string) {
	t.Helper()
	if e == nil || !strings.Contains(e.Error(), text) {
		t.Fatalf("expected %q error, got %v", text, e)
	}
}
func parsed(t *testing.T, s string) M {
	t.Helper()
	v, e := StrictJSON([]byte(s))
	require(t, e)
	return obj(v)
}

type fixture struct {
	s    *Store
	root string
}

func newFixture(t *testing.T, extra M) fixture {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	require(t, e)
	root := filepath.Join(dir, "files")
	require(t, os.Mkdir(root, 0700))
	for p, b := range map[string]string{"requirement.json": `{"pressure_kpa":100}`, "delivery.md": "Check pressure before delivery.\n", "machine.md": "机器故障请先断电检查。\nRestart damaged machines safely.\n", "spending.csv": "id,amount\nA,100\nB,200\n"} {
		require(t, os.WriteFile(filepath.Join(root, p), []byte(b), 0600))
	}
	s, e := Open(filepath.Join(dir, "private", "state.db"))
	require(t, e)
	t.Cleanup(func() { s.Close() })
	spec := parsed(t, `{"id":"p","name":"Synthetic","dependencies":{"delivery.md":["requirement.json"]},"checks":[{"id":"pressure","file":"requirement.json","pointer":"/pressure_kpa","op":"gte","expected":0}]}`)
	for k, v := range extra {
		spec[k] = v
	}
	_, e = s.Create(spec, root, editor)
	require(t, e)
	_, e = s.Sync("p", editor)
	require(t, e)
	return fixture{s, root}
}
func (f fixture) base(t *testing.T) string {
	t.Helper()
	v, e := f.s.Version("p", "latest", nil, agent)
	require(t, e)
	return v.ID
}
func (f fixture) call(t *testing.T, op string, q M) M {
	t.Helper()
	v, e := f.s.Dispatch("p", op, q, agent)
	require(t, e)
	return v
}
func write(base, id string, changes M) M {
	return M{"base_version": base, "request_id": id, "message": "Synthetic change", "changes": changes}
}
func (f fixture) save(t *testing.T, id string, changes M) M {
	t.Helper()
	v, e := f.s.Write("p", write(f.base(t), id, changes), agent)
	require(t, e)
	return v
}
func dataContract(t *testing.T) M {
	return parsed(t, `{"meaning":{"unit":"CNY","population":"card_panel"},"allowed_uses":["panel_spending"],"checks":[{"id":"rows","op":"row_count","minimum":1},{"id":"ids","op":"unique","column":"id"},{"id":"amount","op":"range","column":"amount","minimum":0}]}`)
}

func TestWorkspaceSaveCASHistoryPublicationAndSemanticMeta(t *testing.T) {
	f := newFixture(t, M{})
	base := f.base(t)
	old, e := f.s.Version("p", base, nil, agent)
	require(t, e)
	q := write(base, "save", M{"requirement.json": M{"text": `{"pressure_kpa":120}`}, "delivery.md": M{"text": "Check pressure 120 before delivery."}, "nested/new.txt": M{"base64": base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 255})}})
	saved, e := f.s.Write("p", q, agent)
	require(t, e)
	id := str(saved["version"])
	assert(t, flag(saved["saved"]) && !flag(saved["published"]), "write must save, not publish")
	again, e := f.s.Write("p", q, agent)
	require(t, e)
	assert(t, equal(saved, again), "retry changed receipt")
	_, e = f.s.Write("p", write(base, "stale", M{"delivery.md": M{"text": "stale"}}), agent)
	wantError(t, e, "STALE")
	q["message"] = "different"
	_, e = f.s.Write("p", q, agent)
	wantError(t, e, "Idempotency")
	historic := f.call(t, "read", M{"path": "requirement.json", "as_of": *old.AvailableAt})
	assert(t, historic["text"] == `{"pressure_kpa":100}`, "historical read leaked future bytes")
	_, e = f.s.Version("p", id, *old.AvailableAt, agent)
	wantError(t, e, "not available")
	_, e = f.s.Version("p", "latest", "2999-01-01T00:00:00Z", agent)
	wantError(t, e, "future")
	diff := f.call(t, "diff", M{"before": base, "after": id})
	assert(t, integer(obj(diff["summary"])["changed"]) == 3, "diff paths")
	impact := f.call(t, "impact", M{"paths": []any{"requirement.json"}})
	assert(t, contains(impact["affected"], "delivery.md"), "dependency impact missing")
	_, e = f.s.Review("p", id, agent)
	wantError(t, e, "Role required")
	_, e = f.s.Review("p", id, testActor("agent", "reviewer"))
	wantError(t, e, "independent")
	_, e = f.s.Publish("p", id, nil, publisher)
	wantError(t, e, "approval")
	_, e = f.s.Review("p", id, reviewer)
	require(t, e)
	_, e = f.s.Publish("p", id, nil, publisher)
	require(t, e)
	_, e = f.s.Publish("p", id, nil, publisher)
	wantError(t, e, "Active release")
	r := f.call(t, "read", M{"path": "delivery.md"})
	assert(t, obj(r["meta"])["semantic_change"] != nil, "semantic change meta absent")
	_, e = f.s.Revoke("p", id, publisher)
	require(t, e)
	_, e = f.s.Version("p", "latest", nil, agent)
	wantError(t, e, "revoked")
	trace, e := f.s.Trace("p", nil, agent)
	require(t, e)
	assert(t, flag(trace["chain_verified"]), "audit not verified")
}
func TestWorkspacePreviewQualityCandidatesAndMetadataFreshness(t *testing.T) {
	f := newFixture(t, M{})
	base := f.base(t)
	q := write(base, "preview", M{"requirement.json": M{"text": `{"pressure_kpa":150}`, "meta": M{"summary": "unsaved"}}})
	q["dry_run"] = true
	r, e := f.s.Write("p", q, agent)
	require(t, e)
	assert(t, !flag(r["saved"]) && equal(r["availability"], nil) && f.base(t) == base, "preview became latest")
	q["dry_run"] = false
	_, e = f.s.Write("p", q, agent)
	wantError(t, e, "Idempotency")
	q = write(base, "blocked", M{"requirement.json": M{"text": `{"pressure_kpa":-1}`}})
	q["require_pass"] = true
	r, e = f.s.Write("p", q, agent)
	require(t, e)
	assert(t, r["status"] == "blocked" && f.base(t) == base, "require-pass wrote failing files")
	_, e = f.s.Sync("p", editor)
	require(t, e)
	r = f.call(t, "read", M{"path": "requirement.json"})
	assert(t, r["metadata"] == nil, "candidate metadata leaked")
	f.save(t, "meta", M{"requirement.json": M{"meta": M{"summary": "unique-metadata-token", "facts": M{"pressure": 100}}}})
	search := f.call(t, "search", M{"query": "unique-metadata-token", "mode": "exact"})
	assert(t, len(list(search["hits"])) > 0, "metadata not searchable")
	r = f.save(t, "modify", M{"requirement.json": M{"text": `{"pressure_kpa":120}`}})
	assert(t, obj(r["verification"])["decision"] == "BLOCKED", "stale fact not blocked")
	search = f.call(t, "search", M{"query": "unique-metadata-token", "mode": "exact"})
	assert(t, len(list(search["hits"])) == 0, "stale metadata indexed")
	r = f.call(t, "read", M{"path": "requirement.json"})
	assert(t, !flag(r["metadata_current"]) && r["declared_by"] == "agent", "human provenance lost")
}
func TestWorkspaceExactQualityFullTablesAndImmutableContracts(t *testing.T) {
	contract, e := Contract(dataContract(t))
	require(t, e)
	var b strings.Builder
	b.WriteString("id,amount\n")
	for i := 0; i < 160; i++ {
		n := 100
		if i == 150 {
			n = -1
		}
		fmt.Fprintf(&b, "p%d,%d\n", i, n)
	}
	report := Quality("table.csv", []byte(b.String()), contract, nil, nil)
	tests := list(report["tests"])
	assert(t, report["decision"] == "BLOCKED" && equal(obj(tests[2])["record_indices"], []int{150}), "did not check full table")
	for _, body := range []string{`[{"id":"A","amount":1,"amount":2}]`, `[{"id":"A","amount":1e999}]`, `[{"id":"A","amount":true}]`} {
		assert(t, Quality("bad.json", []byte(body), contract, nil, nil)["decision"] == "BLOCKED", "malformed JSON passed")
	}
	for _, body := range []string{"id,amount\nA,=SUM(A1)\n", "id,amount\nA,1,2\n", "id,id\nA,1\n"} {
		assert(t, Quality("bad.csv", []byte(body), contract, nil, nil)["decision"] == "BLOCKED", "malformed CSV passed")
	}
	exact, e := Contract(parsed(t, `{"checks":[{"id":"exact","op":"range","column":"amount","minimum":0.100000000000000001,"maximum":0.100000000000000001}]}`))
	require(t, e)
	assert(t, Quality("t.csv", []byte("amount\n0.100000000000000001\n"), exact, nil, nil)["decision"] == "PASS", "precision lost")
	assert(t, Quality("t.csv", []byte("amount\n0.1\n"), exact, nil, nil)["decision"] == "BLOCKED", "rounded number passed")
	f := newFixture(t, M{"data_contracts": M{"spending.csv": contract}})
	q := write(f.base(t), "lower", M{"spending.csv": M{"text": "id,amount\nA,-1\n", "meta": M{"data": M{}}}})
	q["require_pass"] = true
	r, e := f.s.Write("p", q, agent)
	require(t, e)
	assert(t, r["status"] == "blocked", "immutable project contract lowered")
	q = write(f.base(t), "delete-required", M{"spending.csv": M{"delete": true}})
	q["require_pass"] = true
	r, e = f.s.Write("p", q, agent)
	require(t, e)
	assert(t, r["status"] == "blocked", "required data deleted")
	use := f.call(t, "quality", M{"paths": []any{"spending.csv"}, "requirements": M{"spending.csv": M{"expect": M{"unit": "USD"}, "purpose": "all_retail"}}})
	assert(t, use["decision"] == "BLOCKED", "incompatible use passed")
}
func TestWorkspaceCohortBaselineAndPinnedLineage(t *testing.T) {
	c := parsed(t, `{"checks":[{"id":"cohort","op":"distinct_count_change","column":"id","max_change":0.2}]}`)
	f := newFixture(t, M{"data_contracts": M{"spending.csv": c}})
	first := f.base(t)
	require(t, os.WriteFile(filepath.Join(f.root, "spending.csv"), []byte("id,amount\nA,100\nB,200\nC,300\n"), 0600))
	_, e := f.s.Sync("p", editor)
	require(t, e)
	v, e := f.s.Version("p", "latest", nil, agent)
	require(t, e)
	assert(t, obj(v.File("spending.csv")["data_quality"])["baseline"] == first, "wrong cohort baseline")
	_, e = f.s.Sync("p", editor)
	require(t, e)
	v, e = f.s.Version("p", "latest", nil, agent)
	require(t, e)
	assert(t, obj(v.File("spending.csv")["data_quality"])["baseline"] == first, "resync reset failed baseline")
	g := newFixture(t, M{})
	base := g.base(t)
	r := g.save(t, "derived", M{"derived.md": M{"text": "Derived result", "meta": M{"processing": "deterministic", "lineage": []any{M{"version": base, "path": "machine.md", "role": "data"}, M{"version": base, "path": "machine.md", "role": "model"}}}}})
	id := str(r["version"])
	v, e = g.s.Version("p", id, nil, agent)
	require(t, e)
	inputs, e := g.s.Lineage("p", M{"derived.md": v.File("derived.md")}, nil, agent)
	require(t, e)
	assert(t, len(inputs) == 2, "role-sensitive lineage collapsed")
	report := g.call(t, "verify", M{"version": id, "as_of": *v.AvailableAt})
	assert(t, report["decision"] == "NEEDS_REVIEW", "historical model claim passed")
}
func TestWorkspaceRetrievalTasksAndEvidenceRevocation(t *testing.T) {
	f := newFixture(t, M{})
	for _, q := range []M{{"query": "pressure", "mode": "hybrid"}, {"query": "机器故障", "mode": "lexical"}} {
		r := f.call(t, "search", q)
		assert(t, len(list(r["hits"])) > 0, "retrieval has no hits")
	}
	_, e := f.s.Dispatch("p", "search", M{"query": "x", "mode": "semantic"}, agent)
	wantError(t, e, "not configured")
	checks := []any{M{"id": "pressure", "object_id": "result", "field": "pressure", "expected": 120}}
	task := f.call(t, "compile", M{"goal": "Check pressure", "paths": []any{"requirement.json"}, "output_checks": checks})
	r := f.call(t, "verify", M{"phase": "postflight", "task_id": task["task_id"], "result": M{"pressure": 120}})
	assert(t, r["decision"] == "PASS", "output oracle did not pass")
	r = f.call(t, "verify", M{"phase": "postflight", "task_id": task["task_id"], "result": M{"pressure": 121}})
	assert(t, r["decision"] == "BLOCKED", "wrong output passed")
	task = f.call(t, "compile", M{"goal": "zzzz-unfindable-zzz", "query": "zzzz-unfindable-zzz"})
	r = f.call(t, "verify", M{"task_id": task["task_id"]})
	assert(t, r["decision"] == "BLOCKED", "no evidence task passed")
	v, e := f.s.Version("p", "latest", nil, agent)
	require(t, e)
	source := str(v.File("requirement.json")["source_id"])
	f.save(t, "evidence", M{"derived.md": M{"text": "Derived statement", "meta": M{"evidence": []any{M{"source_id": source, "locator": "line:1", "quote": "100"}}}}})
	_, e = f.s.Policy("p", source, []string{"reader", "editor", "reviewer"}, true, reviewer)
	require(t, e)
	_, e = f.s.Version("p", "latest", nil, agent)
	wantError(t, e, "revoked")
	trace, e := f.s.Trace("p", nil, agent)
	require(t, e)
	assert(t, len(list(trace["events"])) == 0, "trace leaked revoked evidence")
}
func TestWorkspaceRecoveryPreservesExternalEditsAndMoveInode(t *testing.T) {
	f := newFixture(t, M{})
	base := f.base(t)
	before, e := f.s.Version("p", base, nil, agent)
	require(t, e)
	q := write(base, "interrupted", M{"delivery.md": M{"text": "replacement"}})
	q["dry_run"] = true
	r, e := f.s.Write("p", q, agent)
	require(t, e)
	id := str(r["version"])
	_, e = f.s.DB.Exec("UPDATE versions SET status='applying' WHERE id=?", id)
	require(t, e)
	require(t, os.WriteFile(filepath.Join(f.root, "delivery.md"), []byte("replacement"), 0600))
	_, e = f.s.Recover("p", id, editor)
	wantError(t, e, "own interrupted")
	_, e = f.s.Recover("p", id, agent)
	require(t, e)
	assert(t, matches(f.root, parsed(t, `{"includes":["**"]}`), before) == nil, "recovery did not restore originals")
	_, e = f.s.DB.Exec("UPDATE versions SET status='applying' WHERE id=?", id)
	require(t, e)
	require(t, os.WriteFile(filepath.Join(f.root, "delivery.md"), []byte("external edit"), 0600))
	_, e = f.s.Recover("p", id, agent)
	wantError(t, e, "preserve external edits")
	b, e := os.ReadFile(filepath.Join(f.root, "delivery.md"))
	require(t, e)
	assert(t, string(b) == "external edit", "external edit overwritten")
	path := filepath.Join(f.root, "machine.md")
	info, e := os.Stat(path)
	require(t, e)
	body, e := os.ReadFile(path)
	require(t, e)
	require(t, Move(f.root, "machine.md", "renamed.md", Hash(body)))
	after, e := os.Stat(filepath.Join(f.root, "renamed.md"))
	require(t, e)
	assert(t, os.SameFile(info, after), "move replaced inode")
	require(t, Move(f.root, "renamed.md", "machine.md", Hash(body)))
}
func TestWorkspacePrivateFilesBoundariesAndIntegrity(t *testing.T) {
	f := newFixture(t, M{})
	base := f.base(t)
	bad := agent
	bad.WorkspaceProjects = []string{"other"}
	_, e := f.s.Version("p", base, nil, bad)
	wantError(t, e, "workspace grant")
	for _, p := range []string{"../outside", "a//b", "a/./b", "/absolute", ".env", "nested/tokens.json"} {
		_, e := f.s.Write("p", write(base, "bad-path", M{p: M{"text": "bad"}}), agent)
		assert(t, e != nil, "unsafe path accepted")
	}
	require(t, os.Symlink("/etc/passwd", filepath.Join(f.root, "outside.txt")))
	_, _, e = ReadFile(f.root, "outside.txt")
	assert(t, e != nil, "symlink read")
	require(t, unix.Mkfifo(filepath.Join(f.root, "pipe.txt"), 0600))
	_, _, e = ReadFile(f.root, "pipe.txt")
	assert(t, e != nil, "FIFO read")
	require(t, os.WriteFile(filepath.Join(f.root, ".env"), []byte("synthetic omitted value"), 0600))
	files, e := Scan(f.root, parsed(t, `{"includes":["**"]}`))
	require(t, e)
	assert(t, files[".env"] == nil && files["outside.txt"] == nil, "scan exposed excluded path")
	_, e = f.s.DB.Exec("UPDATE versions SET availability_hash='bad' WHERE id=?", base)
	require(t, e)
	_, e = f.s.Version("p", base, nil, agent)
	wantError(t, e, "Availability integrity")
	dir, e := filepath.EvalSymlinks(t.TempDir())
	require(t, e)
	legacy := filepath.Join(dir, "legacy.db")
	db, e := sql.Open("sqlite", legacy)
	require(t, e)
	_, e = db.Exec("CREATE TABLE old_state (id TEXT)")
	require(t, e)
	require(t, db.Close())
	require(t, os.Chmod(legacy, 0600))
	before, e := os.ReadFile(legacy)
	require(t, e)
	_, e = Open(legacy)
	wantError(t, e, "Legacy")
	after, e := os.ReadFile(legacy)
	require(t, e)
	assert(t, bytes.Equal(before, after), "legacy database mutated")
}
func TestWorkspaceScopedHTTPAndCredentialFiles(t *testing.T) {
	f := newFixture(t, M{})
	token := strings.Repeat("a", 64)
	reader := agent
	reader.Roles = []string{"reader"}
	handler, e := Handler(f.s, Tokens{token: reader})
	require(t, e)
	server := httptest.NewServer(handler)
	defer server.Close()
	call := func(method, path, body string, auth bool) (int, M) {
		t.Helper()
		r, e := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		require(t, e)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, e := http.DefaultClient.Do(r)
		require(t, e)
		defer response.Body.Close()
		var value any
		require(t, jsonDecoder(response.Body, &value))
		return response.StatusCode, obj(value)
	}
	status, _ := call("POST", "/api/workspaces/p/ls", `{}`, false)
	assert(t, status == 401, "missing token accepted")
	status, _ = call("POST", "/api/workspaces/other/ls", `{}`, true)
	assert(t, status == 403, "cross-project access")
	status, _ = call("POST", "/api/workspaces/p/write", `{}`, true)
	assert(t, status == 403, "reader wrote")
	status, _ = call("POST", "/api/projects/p/versions/whatever/publish", `{}`, true)
	assert(t, status == 403, "agent published")
	status, _ = call("POST", "/api/workspaces/p/ls", `{"version":"latest","version":"published"}`, true)
	assert(t, status == 422, "duplicate JSON accepted")
	status, r := call("POST", "/api/workspaces/p/ls", `{}`, true)
	assert(t, status == 200 && r["version"] != nil, "scoped read failed")
	path := filepath.Join(filepath.Dir(f.s.Path), "tokens.json")
	_, e = AuthInit(path)
	require(t, e)
	before, e := ReadTokens(path)
	require(t, e)
	result, e := f.s.AuthAgent("p", path, true, editor)
	require(t, e)
	after, e := ReadTokens(path)
	require(t, e)
	assert(t, len(after) == len(before)+1 && after[str(result["FILEWISE_TOKEN"])].Audience == "agent", "agent token not recorded")
	require(t, os.Chmod(path, 0644))
	_, e = ReadTokens(path)
	wantError(t, e, "private regular")
}
func jsonDecoder(r io.Reader, v *any) error {
	b, e := io.ReadAll(r)
	if e != nil {
		return e
	}
	*v, e = StrictJSON(b)
	return e
}
func TestWorkspaceInputValidation(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `1 2`, `{"n":1e999}`, strings.Repeat("[", 130) + strings.Repeat("]", 130)} {
		_, e := StrictJSON([]byte(raw))
		assert(t, e != nil, "invalid JSON accepted")
	}
	for _, q := range []M{{"max_chars": 99}, {"mode": "invented"}, {"query": ""}, {"paths": []any{"../x"}}, {"include_bytes": "yes"}, {"extra": true}} {
		_, e := Query(q)
		assert(t, e != nil, "invalid query accepted")
	}
	_, e := Metadata(M{"semantic_change": "invented"})
	assert(t, e != nil, "human metadata overwrote derived fields")
	_, e = Metadata(M{"valid_from": "2030-01-01T00:00:00Z", "valid_until": "2029-01-01T00:00:00Z"})
	assert(t, e != nil, "invalid interval")
	a := agent
	a.Roles = append(a.Roles, "publisher")
	wantError(t, a.Validate(), "cannot approve")
}
