//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/huaiwen/filewise/internal/journal"
)

func TestGoGuideExamplesAndRuntimeScope(t *testing.T) {
	blocks := regexp.MustCompile("(?s)```(bash|json)\\n(.*?)```")
	for _, name := range []string{"README.md", "README.en.md", "docs/go.md", "docs/go-migration.md", "docs/install.md", "docs/folders.md", "docs/agent-skills.md"} {
		body, err := os.ReadFile(filepath.Join("../..", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range blocks.FindAllSubmatch(body, -1) {
			if string(m[1]) == "json" {
				var value any
				if json.Unmarshal(m[2], &value) != nil {
					t.Fatal("invalid JSON example", name)
				}
				continue
			}
			command := exec.Command("bash", "-n")
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
			command.Stdin = bytes.NewReader(m[2])
			if err := command.Run(); err != nil {
				t.Fatal("invalid Bash example", name, err)
			}
		}
	}
	for _, name := range []string{"rust", "Cargo.toml", "Cargo.lock"} {
		if _, err := os.Stat(filepath.Join("../..", name)); !os.IsNotExist(err) {
			t.Fatal("superseded runtime remains active", name)
		}
	}
	for _, name := range []string{"Makefile", "Dockerfile", ".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		body, err := os.ReadFile(filepath.Join("../..", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"cargo build", "cargo test", "rust-toolchain", "FROM rust:"} {
			if bytes.Contains(body, []byte(command)) {
				t.Fatal("build still requires retired runtime", name)
			}
		}
	}
}

func TestCLIWordCompareCaptureAndValidation(t *testing.T) {
	t.Setenv("FILEWISE_TOKEN", "")
	t.Setenv("FILEWISE_URL", "http://127.0.0.1:1")
	var out, diagnostic bytes.Buffer
	call := func(args ...string) error { out.Reset(); diagnostic.Reset(); return run(args, &out, &diagnostic) }
	before := "../../site/examples/acceptance-zh-before.docx"
	after := "../../site/examples/acceptance-zh-after.docx"
	if err := call("document", "inspect", before); err != nil || !strings.Contains(out.String(), "word/document.xml/paragraph:3") {
		t.Fatal(out.String(), err)
	}
	if err := call("document", "compare", "--before", before, "--after", after); err != nil || !strings.Contains(out.String(), `"delta": "+20"`) {
		t.Fatal(out.String(), err)
	}
	root := t.TempDir()
	state, file := filepath.Join(root, "state"), filepath.Join(root, "acceptance.docx")
	a, err := os.ReadFile(before)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(after)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, a, 0600); err != nil {
		t.Fatal(err)
	}
	if err = call("document", "capture", "--state", state, "--base", "none", file); err != nil {
		t.Fatal(err)
	}
	var first journal.Receipt
	if err = json.Unmarshal(out.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = call("document", "capture", "--state", state, "--base", first.ID, file); err != nil || !strings.Contains(out.String(), `"semantic_change"`) {
		t.Fatal(out.String(), err)
	}
	if err = call("document", "record", "--state", state, "--file", file, "--version", first.ID); err != nil || !strings.Contains(out.String(), first.ID) {
		t.Fatal(out.String(), err)
	}
	if err = call("document", "record", "--state", filepath.Join(root, "absent"), "--file", file); err == nil {
		t.Fatal("read initialized state")
	}
	if _, err = os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
		t.Fatal("read mutated state")
	}
	for _, args := range [][]string{{"agent", "projects"}, {"not-a-command"}, {"project", "publish"}, {"document", "capture", "--state", state, file}, {"document", "inspect"}, {"document", "compare", "--before", before}} {
		if err = call(args...); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if err = call("--version"); err != nil || out.String() != "filewise "+version+" (Go)\n" {
		t.Fatal(out.String(), err)
	}
	if err = call("--licenses"); err != nil || !strings.Contains(out.String(), "modernc.org/sqlite") || !strings.Contains(out.String(), "github.com/ledongthuc/pdf") {
		t.Fatal("embedded notices missing", err)
	}
	if err = call("--help"); err != nil || !strings.Contains(out.String(), "Working saves do not approve or publish") {
		t.Fatal(out.String(), err)
	}
}
