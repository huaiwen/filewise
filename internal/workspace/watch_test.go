//go:build darwin || linux

package workspace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func watched(t *testing.T, rules M) (*Store, string, string) {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	require(t, e)
	root := filepath.Join(dir, "files")
	require(t, os.Mkdir(root, 0700))
	require(t, os.WriteFile(filepath.Join(root, "download.md"), []byte("# Equipment acceptance\nTest pressure must reach 100 kPa.\n"), 0600))
	s, e := Open(filepath.Join(dir, "private", "state.db"))
	require(t, e)
	t.Cleanup(func() { s.Close() })
	r, e := s.Register(M{"root": root, "name": "Synthetic folder", "rules": rules}, editor)
	require(t, e)
	return s, root, str(r["project"])
}
func jobsFor(t *testing.T, s *Store) []M {
	t.Helper()
	bodies, e := s.stringRows("SELECT bundle FROM watch_jobs ORDER BY rowid")
	require(t, e)
	jobs := []M{}
	for _, b := range bodies {
		jobs = append(jobs, parsed(t, b))
	}
	return jobs
}
func settle(t *testing.T, s *Store, p string) {
	t.Helper()
	require(t, s.WatchScan(p, 100))
	require(t, s.WatchTick(104))
}
func TestWatchStableFilesMetadataRenameUndoAndRestartReceipts(t *testing.T) {
	s, root, p := watched(t, M{"naming": "auto", "template": "{title}"})
	before, e := os.Stat(filepath.Join(root, "download.md"))
	require(t, e)
	require(t, os.WriteFile(filepath.Join(root, "secret.tmp"), []byte("unfinished"), 0600))
	settle(t, s, p)
	jobs := jobsFor(t, s)
	assert(t, len(jobs) == 1, "download suffix was captured")
	j := jobs[0]
	assert(t, j["status"] == "done" && j["output_path"] == "Equipment acceptance.md", "automatic naming did not finish")
	after, e := os.Stat(filepath.Join(root, str(j["output_path"])))
	require(t, e)
	assert(t, os.SameFile(before, after), "rename lost inode")
	_, _, active, e := s.Project(p, editor)
	require(t, e)
	assert(t, active == nil, "automation published")
	require(t, s.WatchTick(108))
	assert(t, len(jobsFor(t, s)) == 1, "self-change triggered a job loop")
	j["status"] = "processing"
	require(t, s.saveJob(j))
	require(t, s.ResumeWatch())
	require(t, s.WatchTick(112))
	j = jobsFor(t, s)[0]
	assert(t, j["status"] == "done", "completed write receipt did not resume")
	r, e := s.WatchAction(str(j["id"]), "undo", editor)
	require(t, e)
	assert(t, r["status"] == "undone", "undo did not finish")
	restored, e := os.Stat(filepath.Join(root, "download.md"))
	require(t, e)
	assert(t, os.SameFile(before, restored), "undo lost inode")
	j = jobsFor(t, s)[0]
	j["status"] = "undoing"
	require(t, s.saveJob(j))
	require(t, s.ResumeWatch())
	require(t, s.WatchTick(116))
	assert(t, jobsFor(t, s)[0]["status"] == "undone", "undo receipt did not resume")
	require(t, s.WatchTick(120))
	assert(t, len(jobsFor(t, s)) == 1, "undo loop")
}
func TestWatchSuggestionsManualOwnershipRulesCASAndOptOut(t *testing.T) {
	s, root, p := watched(t, M{"naming": "suggest", "template": "{title}"})
	settle(t, s, p)
	j := jobsFor(t, s)[0]
	assert(t, j["status"] == "review" && j["output_path"] == "download.md", "suggestion renamed prematurely")
	v, e := s.Version(p, "latest", nil, editor)
	require(t, e)
	assert(t, v.File("download.md")["declared_by"] == "filewise-watcher", "suggestion did not save metadata")
	w, e := s.watch(p)
	require(t, e)
	rules := clone(obj(w["rules"]))
	rules["enabled"] = false
	_, e = s.Configure(p, rules, nil, editor)
	wantError(t, e, "rules changed")
	_, e = s.Configure(p, rules, w["revision"], editor)
	require(t, e)
	_, e = s.WatchAction(str(j["id"]), "approve", editor)
	wantError(t, e, "paused")
	rules["enabled"] = true
	w, e = s.watch(p)
	require(t, e)
	_, e = s.Configure(p, rules, w["revision"], editor)
	require(t, e)
	_, e = s.WatchAction(str(j["id"]), "retry", editor)
	require(t, e)
	require(t, s.WatchScan(p, 200))
	require(t, s.WatchTick(204))
	jobs := jobsFor(t, s)
	j = jobs[len(jobs)-1]
	assert(t, j["status"] == "review", "retry did not reanalyze")
	_, e = s.WatchAction(str(j["id"]), "approve", editor)
	require(t, e)
	require(t, s.WatchTick(208))
	j = jobsFor(t, s)[len(jobs)-1]
	assert(t, j["status"] == "done", "approved rename failed")
	v, e = s.Version(p, "latest", nil, editor)
	require(t, e)
	path := str(j["output_path"])
	_, e = s.Write(p, write(v.ID, "manual", M{path: M{"meta": M{"summary": "Human declaration"}}}), editor)
	require(t, e)
	_, e = s.WatchAction(str(j["id"]), "undo", editor)
	wantError(t, e, "Undo conflict")
	_, e = s.WatchAction(str(j["id"]), "retry", editor)
	require(t, e)
	require(t, s.WatchScan(p, 300))
	require(t, s.WatchTick(304))
	jobs = jobsFor(t, s)
	last := jobs[len(jobs)-1]
	assert(t, last["status"] == "failed" && strings.Contains(str(last["error"]), "Manual metadata"), "manual metadata overwritten")
	b, e := os.ReadFile(filepath.Join(root, path))
	require(t, e)
	assert(t, strings.Contains(string(b), "100 kPa"), "content mutated")
	other, _, project := watched(t, M{"process_existing": false})
	settle(t, other, project)
	assert(t, len(jobsFor(t, other)) == 0, "existing opt-out ignored")
}
func TestWatchRootReplacementRevocationAndModelTransport(t *testing.T) {
	s, root, p := watched(t, M{})
	settle(t, s, p)
	j := jobsFor(t, s)[0]
	v, e := s.Version(p, str(j["version"]), nil, editor)
	require(t, e)
	source := str(v.File("download.md")["source_id"])
	_, e = s.Policy(p, source, []string{"reader", "editor", "reviewer"}, true, reviewer)
	require(t, e)
	overview, e := s.Overview(editor)
	require(t, e)
	assert(t, obj(list(overview["jobs"])[0])["status"] == "unavailable", "revoked source exposed in activity")
	require(t, os.Rename(root, root+"-old"))
	require(t, os.Mkdir(root, 0700))
	wantError(t, s.WatchScan(p, 200), "root was replaced")
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert(t, r.URL.Path == "/api/chat", "model endpoint path")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(JSON(M{"message": M{"content": string(JSON(M{"title": "Local title", "summary": "Grounded local summary", "tags": []any{"local"}, "fields": M{}}))}}))
	}))
	defer model.Close()
	a, e := Analyze("test.md", []byte("# Original title\nSynthetic text."), M{"engine": "ollama", "model": "mock", "endpoint": model.URL})
	require(t, e)
	assert(t, a["title"] == "Local title" && a["coverage"] == "model_text", "local model was not used")
	_, e = Rules(M{"engine": "ollama", "model": "mock", "endpoint": "https://example.com"})
	wantError(t, e, "loopback")
	_, e = Analyze("test.md", []byte("text"), M{"fields": M{"amount": "/amount"}})
	wantError(t, e, "JSON field")
}
func TestLocalHTTPBoundaryAndEmbeddedUI(t *testing.T) {
	f := newFixture(t, M{})
	token := strings.Repeat("u", 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &Gateway{Store: f.s, Tokens: Tokens{token: editor}, Local: &Local{URL: "http://127.0.0.1:8733", Instance: randomID(), Stop: cancel}}
	call := func(method, path, host, origin, bearer string) (int, M, string) {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:8733"+path, strings.NewReader("{}"))
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		var m M
		if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			m = parsed(t, w.Body.String())
		}
		return w.Code, m, w.Body.String()
	}
	code, _, body := call("GET", "/", "127.0.0.1:8733", "", "")
	assert(t, code == 200 && strings.Contains(body, "Filewise"), "embedded workbench missing")
	code, _, _ = call("GET", "/", "evil.invalid", "", "")
	assert(t, code == 403, "Host rebinding allowed")
	code, _, _ = call("POST", "/api/local/overview", "127.0.0.1:8733", "https://evil.invalid", token)
	assert(t, code == 403, "cross-origin request allowed")
	code, r, _ := call("GET", "/api/local/status", "127.0.0.1:8733", "", token)
	assert(t, code == 200 && r["instance"] == g.Local.Instance, "service status mismatch")
	code, _, _ = call("POST", "/api/local/stop", "127.0.0.1:8733", "", token)
	assert(t, code == 200 && ctx.Err() != nil, "stop did not signal")
}
