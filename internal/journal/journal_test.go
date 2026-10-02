//go:build darwin || linux

package journal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func put(t *testing.T, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(name, body, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestCaptureWordMetaCASHistoryAndOriginalIntegrity(t *testing.T) {
	root := t.TempDir()
	file, state := filepath.Join(root, "验收要求.docx"), filepath.Join(root, "private")
	a, err := os.ReadFile("../../site/examples/acceptance-zh-before.docx")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../site/examples/acceptance-zh-after.docx")
	if err != nil {
		t.Fatal(err)
	}
	put(t, file, a)
	j, err := Open(state, file)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	first, err := j.Capture("none")
	if err != nil {
		t.Fatal(err)
	}
	same, err := j.Capture(first.ID)
	if err != nil || !same.Unchanged || same.ID != first.ID {
		t.Fatalf("%+v %v", same, err)
	}
	if _, err = j.Capture("none"); err == nil {
		t.Fatal("missing CAS check")
	}
	put(t, file, b)
	second, err := j.Capture(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.Parent != first.ID || second.Record.Meta.SemanticChange.Changes[0].Quantity.Delta != "+20" {
		t.Fatal(second)
	}
	if old, err := j.Read(first.ID); err != nil || old.Record.SHA256 != first.Record.SHA256 {
		t.Fatal(old, err)
	}
	if _, err = j.Capture(first.ID); err == nil {
		t.Fatal("stale capture accepted")
	}
	actual, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(actual, b) {
		t.Fatal("original modified")
	}
	encoded, err := json.Marshal(second)
	if err != nil || !bytes.Contains(encoded, []byte(`"meta":{"semantic_change":`)) {
		t.Fatal("semantic meta missing")
	}
	// A crash after object writes but before pointer replacement leaves the old
	// latest authoritative. A complete owned pending pointer is safely replaced.
	put(t, filepath.Join(state, "latest.pending"), []byte(first.ID))
	put(t, file, []byte("not a valid Word archive"))
	third, err := j.Capture(second.ID)
	if err != nil || third.Record.Meta.SemanticChange.Status != "incomplete" {
		t.Fatal(third, err)
	}
	raw, err := os.ReadFile(filepath.Join(state, "blobs", third.Record.SHA256))
	if err != nil || string(raw) != "not a valid Word archive" {
		t.Fatal("failed extraction lost original")
	}
	if latest, err := j.Read("latest"); err != nil || latest.ID != third.ID {
		t.Fatal(latest, err)
	}
	// Verify source corruption, then restore only the test-owned object.
	blob := filepath.Join(state, "blobs", second.Record.SHA256)
	put(t, blob, []byte("tampered"))
	if _, err = j.Read(second.ID); err == nil {
		t.Fatal("corrupt original accepted")
	}
	put(t, blob, b)
	record := filepath.Join(state, "records", second.ID+".json")
	saved, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	put(t, record, []byte("{}"))
	if _, err = j.Read(second.ID); err == nil {
		t.Fatal("corrupt metadata accepted")
	}
	put(t, record, saved)
	if _, err = j.Read("../../secrets"); err == nil {
		t.Fatal("invalid version accepted")
	}
}
func TestPrivateStateAndUnsafeInputAreRejectedWithoutLegacyMutation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "note.md")
	put(t, file, []byte("hello"))
	legacy := filepath.Join(root, "legacy")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(legacy, "filewise.db"), []byte("legacy"))
	if j, err := Open(legacy, file); err == nil {
		j.Close()
		t.Fatal("legacy accepted")
	}
	entries, err := os.ReadDir(legacy)
	if err != nil || len(entries) != 1 {
		t.Fatal("legacy directory changed")
	}
	unsafe := filepath.Join(root, "unsafe")
	if err = os.Mkdir(unsafe, 0755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(unsafe, 0755)
	if j, err := Open(unsafe, file); err == nil {
		j.Close()
		t.Fatal("public state accepted")
	}
	link := filepath.Join(root, "linked")
	if err = os.Symlink(legacy, link); err != nil {
		t.Fatal(err)
	}
	if j, err := Open(link, file); err == nil {
		j.Close()
		t.Fatal("state link accepted")
	}
	sourceLink := filepath.Join(root, "source-link")
	if err = os.Symlink(file, sourceLink); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadFile(sourceLink); err == nil {
		t.Fatal("source link accepted")
	}
	fifo := filepath.Join(root, "fifo")
	if err = syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadFile(fifo); err == nil {
		t.Fatal("FIFO accepted")
	}
	state := filepath.Join(root, "state")
	j, err := Open(state, file)
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.Capture("none")
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	if j, err = Open(state, filepath.Join(root, "other.md")); err == nil {
		j.Close()
		t.Fatal("path rebound")
	}
	latest := filepath.Join(state, "latest")
	if err = os.Remove(latest); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(file, latest); err != nil {
		t.Fatal(err)
	}
	j, err = Open(state, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.Capture(first.ID); err == nil {
		t.Fatal("linked pointer accepted")
	}
	j.Close()
	if err = os.Remove(latest); err != nil {
		t.Fatal(err)
	}
	put(t, latest, []byte(first.ID))
	if err = os.Link(latest, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	j, err = Open(state, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.Read("latest"); err == nil {
		t.Fatal("hard-linked private pointer accepted")
	}
	j.Close()
}
func TestConcurrentCapturesUseOneCrossProcessCompatibleLock(t *testing.T) {
	root := t.TempDir()
	file, state := filepath.Join(root, "note.md"), filepath.Join(root, "state")
	put(t, file, []byte("Pressure: 100 kPa"))
	j, err := Open(state, file)
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.Capture("none")
	j.Close()
	if err != nil {
		t.Fatal(err)
	}
	put(t, file, []byte("Pressure: 120 kPa"))
	result := make(chan error, 2)
	for range 2 {
		go func() {
			j, err := Open(state, file)
			if err != nil {
				result <- err
				return
			}
			defer j.Close()
			_, err = j.Capture(first.ID)
			result <- err
		}()
	}
	good, stale := 0, 0
	for range 2 {
		select {
		case err := <-result:
			if err == nil {
				good++
			} else if strings.Contains(err.Error(), "STALE") {
				stale++
			} else {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("capture lock did not release")
		}
	}
	if good != 1 || stale != 1 {
		t.Fatalf("good=%d stale=%d", good, stale)
	}
}
