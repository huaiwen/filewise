//go:build darwin || linux

package documents

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// The test executable implements bounded adversarial worker responses without
// invoking a shell, leaving descendants, or applying limits to the test runner.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "__document_worker" {
		body, _ := io.ReadAll(io.LimitReader(os.Stdin, MaxFile+1))
		switch string(body) {
		case "wait":
			time.Sleep(time.Hour)
		case "oversized":
			_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), workerOutput+1))
		case "mismatch":
			_ = json.NewEncoder(os.Stdout).Encode(extract("x.txt", []byte("wrong format")))
		case "environment":
			if os.Getenv("FILEWISE_TOKEN") != "" || os.Getenv("HOME") != "" {
				os.Exit(3)
			}
			_ = json.NewEncoder(os.Stdout).Encode(extract("x.pdf", nil))
		default:
			_ = json.NewEncoder(os.Stdout).Encode(extract("input."+os.Args[2], body))
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestWorkerBoundsProtocolCleanupAndEnvironment(t *testing.T) {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	if rss, e := processRSS(os.Getpid()); e != nil || rss == 0 {
		t.Fatal("RSS monitoring unavailable", rss, e)
	}
	for _, body := range []string{"wait", "oversized", "mismatch"} {
		limit := 2 * time.Second
		if body == "wait" {
			limit = 80 * time.Millisecond
		}
		start := time.Now()
		if _, e = runWorker(exe, "pdf", []byte(body), limit, workerRSS); e == nil {
			t.Fatal("worker bound ignored", body)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("worker not killed and waited", body)
		}
	}
	if _, e = runWorker(exe, "pdf", []byte("wait"), time.Second, 1); e == nil || !strings.Contains(e.Error(), "resident-memory") {
		t.Fatal("RSS threshold not enforced", e)
	}
	t.Setenv("FILEWISE_TOKEN", "synthetic-not-a-real-credential")
	if _, e = runWorker(exe, "pdf", []byte("environment"), 3*time.Second, workerRSS); e != nil {
		t.Fatal(e)
	}
	if _, e = runWorker(exe, "pdf", make([]byte, MaxFile+1), time.Second, workerRSS); e == nil {
		t.Fatal("oversized input accepted")
	}
	if _, e = runWorker(exe, "exe", nil, time.Second, workerRSS); e == nil {
		t.Fatal("invalid format accepted")
	}
	// io.Copy must not bypass the output cap through a promoted ReadFrom method.
	capped := &boundedOutput{limit: 8}
	if _, e = io.Copy(capped, strings.NewReader("0123456789")); e == nil || len(capped.Bytes()) > 8 {
		t.Fatal("output cap bypassed", e)
	}
	if ConfigureWorker("relative") == nil {
		t.Fatal("relative worker executable accepted")
	}
	if e = ConfigureWorker(exe); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { workerConfig.Lock(); workerConfig.path = ""; workerConfig.Unlock() })
	if ConfigureWorker(exe) == nil {
		t.Fatal("worker reconfiguration accepted")
	}
	x := Extract("valid.docx", packageBytes(t, `<w:p><w:r><w:t>worker text</w:t></w:r></w:p>`, nil))
	if x.Info.Status != "text" || len(x.Fragments) != 1 || x.Fragments[0].Text != "worker text" {
		t.Fatal(x)
	}
	x.Fragments[0].Text = "mutated by caller"
	// The exact same bytes must return a fresh copy of cached evidence.
	workerCache.Lock()
	var cached []byte
	for _, b := range workerCache.entries {
		cached = b
	}
	workerCache.Unlock()
	if bytes.Contains(cached, []byte("mutated by caller")) {
		t.Fatal("caller changed cached evidence")
	}
}
