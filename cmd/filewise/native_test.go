//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huaiwen/filewise/internal/documents"
	"github.com/huaiwen/filewise/internal/testdocs"
)

func TestNativeGoWorkerBackgroundLifecycleAndWorkbench(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	exe := filepath.Join(root, "filewise")
	build := exec.Command("go", "build", "-trimpath", "-o", exe, "./cmd/filewise")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("native Go build: %v: %s", e, b)
	}
	home := filepath.Join(root, "home")
	if e = os.Mkdir(home, 0700); e != nil {
		t.Fatal(e)
	}
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, exe, args...)
		c.Env = env
		c.Dir = root
		return c.Output()
	}
	object := func(b []byte) map[string]any {
		t.Helper()
		v := map[string]any{}
		if e := json.Unmarshal(b, &v); e != nil {
			t.Fatal("invalid native JSON response", e)
		}
		return v
	}
	call := func(args ...string) map[string]any {
		t.Helper()
		b, e := run(args...)
		if e != nil {
			t.Fatalf("native %s failed: %v", args[0], e)
		}
		return object(b)
	}
	inputs := map[string][]byte{"zh.pdf": testdocs.ChinesePDF(), "paper.pdf": testdocs.PDF("PDF title"), "deck.pptx": testdocs.Slides("Slide title", "More evidence", nil), "table.xlsx": testdocs.Workbook(`<row r="1"><c r="A1" t="inlineStr"><is><t>amount</t></is></c></row><row r="2"><c r="A2"><v>9007199254740993.11</v></c></row>`, nil)}
	word, e := os.ReadFile("../../site/examples/acceptance-en-after.docx")
	if e != nil {
		t.Fatal(e)
	}
	inputs["word.docx"] = word
	folder := filepath.Join(root, "files")
	if e = os.Mkdir(folder, 0700); e != nil {
		t.Fatal(e)
	}
	infos := map[string]os.FileInfo{}
	for name, body := range inputs {
		path := filepath.Join(folder, name)
		if e = os.WriteFile(path, body, 0600); e != nil {
			t.Fatal(e)
		}
		infos[name], e = os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		parsed := call("document", "inspect", path)
		x := parsed["extraction"].(map[string]any)
		if x["info"].(map[string]any)["status"] != "text" || parsed["sha256"] != documents.Hash(body) {
			t.Fatal("native worker extraction or original hash differs", name)
		}
	}
	bad := filepath.Join(folder, "bad.pdf")
	if e = os.WriteFile(bad, []byte("%PDF-broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = run("document", "inspect", bad); e == nil {
		t.Fatal("malformed PDF was accepted")
	}
	// Choose an ephemeral loopback port; a competing bind fails cleanly at start.
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	db := filepath.Join(root, "private", "state.db")
	args := []string{"--db", db}
	startArgs := append(append([]string{}, args...), "start", "--no-open", "--port", strconv.Itoa(port))
	start := call(startArgs...)
	defer func() { _, _ = run(append(args, "stop")...) }()
	u, e := url.Parse(start["url"].(string))
	if e != nil {
		t.Fatal("invalid service URL")
	}
	token := strings.TrimPrefix(u.Fragment, "token=")
	u.Fragment = ""
	origin := strings.TrimSuffix(u.String(), "/")
	if token == "" {
		t.Fatal("local credential missing")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path string, body any, host, requestOrigin string, expected int) []byte {
		t.Helper()
		b, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		r, e := http.NewRequest(method, origin+path, bytes.NewReader(b))
		if e != nil {
			t.Fatal("invalid test request")
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if host != "" {
			r.Host = host
		}
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal("local HTTP request failed")
		}
		defer response.Body.Close()
		b, e = io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if e != nil || response.StatusCode != expected {
			t.Fatalf("local %s: status %d, expected %d", path, response.StatusCode, expected)
		}
		return b
	}
	live := call(append(args, "status")...)
	instance := live["instance"]
	again := call(startArgs...)
	if again["url"] != start["url"] {
		t.Fatal("start was not idempotent")
	}
	request("POST", "/api/local/stop", map[string]any{"instance": strings.Repeat("0", 64)}, "", "", 409)
	request("GET", "/api/local/status", map[string]any{}, "attacker.invalid", "", 403)
	request("GET", "/api/local/status", map[string]any{}, "", "https://attacker.invalid", 403)
	for _, asset := range []string{"/", "/ui/app.js", "/ui/i18n.js", "/ui/strings.json", "/ui/style.css"} {
		if len(request("GET", asset, nil, "", "", 200)) == 0 {
			t.Fatal("empty embedded UI", asset)
		}
	}
	stringsJSON := request("GET", "/ui/strings.json", nil, "", "", 200)
	if !bytes.Contains(stringsJSON, []byte(`"zh-CN"`)) || !bytes.Contains(stringsJSON, []byte(`"en"`)) {
		t.Fatal("workbench locales missing")
	}
	registered := object(request("POST", "/api/local/register", map[string]any{"root": folder, "name": "Native synthetic documents", "rules": map[string]any{"poll_seconds": 1, "settle_seconds": 2, "naming": "auto", "template": "{title}"}}, "", "", 200))
	if registered["project"] == nil {
		t.Fatal("folder registration did not return a project")
	}
	var jobs []any
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		overview := object(request("GET", "/api/local/overview", map[string]any{}, "", "", 200))
		jobs, _ = overview["jobs"].([]any)
		complete := len(jobs) == len(inputs)+1
		for _, raw := range jobs {
			status := raw.(map[string]any)["status"]
			complete = complete && (status == "done" || status == "failed")
		}
		if complete {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(jobs) != len(inputs)+1 {
		t.Fatal("background monitoring did not capture every synthetic file")
	}
	undoID := ""
	for _, raw := range jobs {
		job := raw.(map[string]any)
		name := job["path"].(string)
		if name == "bad.pdf" {
			if job["status"] != "failed" {
				t.Fatal("malformed document not isolated")
			}
			continue
		}
		if job["status"] != "done" {
			t.Fatal("valid document job did not complete", name)
		}
		path := filepath.Join(folder, job["output_path"].(string))
		info, e := os.Stat(path)
		if e != nil || !os.SameFile(infos[name], info) {
			t.Fatal("background document rename lost inode", name)
		}
		if name == "word.docx" {
			undoID = job["id"].(string)
		}
	}
	request("POST", "/api/local/action", map[string]any{"job": undoID, "action": "undo"}, "", "", 200)
	info, e := os.Stat(filepath.Join(folder, "word.docx"))
	if e != nil || !os.SameFile(infos["word.docx"], info) {
		t.Fatal("native undo did not restore original inode")
	}
	if runtime.GOOS == "darwin" {
		enabled := call(append(args, "autostart", "--enable", "--port", strconv.Itoa(port))...)
		p := enabled["path"].(string)
		if !strings.HasPrefix(p, home+string(os.PathSeparator)) {
			t.Fatal("startup escaped synthetic HOME")
		}
		plist, e := os.ReadFile(p)
		if e != nil || bytes.Contains(plist, []byte(token)) || !bytes.Contains(plist, []byte(exe)) {
			t.Fatal("invalid isolated login definition")
		}
		call(append(args, "autostart", "--disable")...)
		if _, e = os.Stat(p); !os.IsNotExist(e) {
			t.Fatal("isolated login definition not removed")
		}
	}
	stopped := call(append(args, "stop")...)
	if stopped["stopped"] != true {
		t.Fatal("background process did not stop")
	}
	if _, e = run(append(args, "status")...); e == nil {
		t.Fatal("stopped daemon still reported alive")
	}
	call(startArgs...)
	live = call(append(args, "status")...)
	if live["instance"] == instance {
		t.Fatal("restart reused process identity")
	}
	overview := object(request("GET", "/api/local/overview", map[string]any{}, "", "", 200))
	if len(overview["folders"].([]any)) != 1 || len(overview["jobs"].([]any)) != len(inputs)+1 {
		t.Fatal("restart lost monitoring history")
	}
	call(append(args, "stop")...)
}
