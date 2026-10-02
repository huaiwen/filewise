//go:build darwin || linux

package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const workerOutput = 24 << 20
const workerRSS = 384 << 20

var workerConfig struct {
	sync.RWMutex
	path string
}

// ponytail: FIFO, 16 entries / 8 MiB serialized; use LRU only if measured reuse needs it.
var workerCache = struct {
	sync.Mutex
	entries map[string][]byte
	order   []string
	size    int
}{entries: map[string][]byte{}}

// ConfigureWorker is called once by the native executable, before any document
// operation. Package tests may use bounded in-process parsing instead.
func ConfigureWorker(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("Document worker requires an absolute executable path")
	}
	workerConfig.Lock()
	defer workerConfig.Unlock()
	if workerConfig.path != "" {
		return errors.New("Document worker executable already configured")
	}
	workerConfig.path = path
	return nil
}
func Extract(name string, body []byte) Extraction {
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	workerConfig.RLock()
	path := workerConfig.path
	workerConfig.RUnlock()
	if path != "" && officeFormat(format) {
		key := format + ":" + Hash(body)
		workerCache.Lock()
		cached := workerCache.entries[key]
		workerCache.Unlock()
		if cached != nil {
			var result Extraction
			d := json.NewDecoder(bytes.NewReader(cached))
			d.UseNumber()
			if d.Decode(&result) == nil {
				return result
			}
		}
		result, e := runWorker(path, format, body, 20*time.Second, workerRSS)
		if e == nil {
			if result.Info.Status != "failed" {
				encoded, err := json.Marshal(result)
				if err == nil && len(encoded) <= 8<<20 {
					workerCache.Lock()
					if workerCache.entries[key] == nil {
						for len(workerCache.order) >= 16 || workerCache.size+len(encoded) > 8<<20 {
							oldest := workerCache.order[0]
							workerCache.order = workerCache.order[1:]
							workerCache.size -= len(workerCache.entries[oldest])
							delete(workerCache.entries, oldest)
						}
						workerCache.entries[key] = encoded
						workerCache.order = append(workerCache.order, key)
						workerCache.size += len(encoded)
					}
					workerCache.Unlock()
				}
			}
			return result
		}
		out := extract(name, nil)
		out.Info.Status = "failed"
		out.Info.Error = e.Error()
		out.Info.Notes = []string{}
		out.Info.FragmentCount = 0
		out.Fragments = []Fragment{}
		out.Sheets = nil
		return out
	}
	return extract(name, body)
}
func officeFormat(format string) bool {
	return format == "docx" || format == "xlsx" || format == "pptx" || format == "pdf"
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }
func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, errors.New("Document worker output limit exceeded")
	}
	return b.buffer.Write(p)
}
func runWorker(executable, format string, body []byte, wall time.Duration, rssLimit uint64) (Extraction, error) {
	var result Extraction
	if !officeFormat(format) {
		return result, errors.New("Invalid document worker format")
	}
	if len(body) > MaxFile {
		return result, errors.New("Document input exceeds 10 MiB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), wall)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "__document_worker", format)
	command.Env = []string{"GOMEMLIMIT=256MiB", "GOGC=80", "GOMAXPROCS=1"}
	command.Dir = "/"
	command.Stdin = bytes.NewReader(body)
	output := &boundedOutput{limit: workerOutput}
	command.Stdout = output
	command.Stderr = io.Discard
	if e := command.Start(); e != nil {
		return result, errors.New("Document worker could not start")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var reason error
	for {
		select {
		case e := <-done:
			if reason != nil {
				return result, reason
			}
			if ctx.Err() != nil {
				return result, errors.New("Document worker wall-time limit exceeded")
			}
			if e != nil {
				return result, errors.New("Document worker failed or exceeded resource limits")
			}
			decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
			decoder.DisallowUnknownFields()
			decoder.UseNumber()
			if e = decoder.Decode(&result); e != nil {
				return Extraction{}, errors.New("Invalid document worker response")
			}
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				return Extraction{}, errors.New("Trailing document worker output")
			}
			if e = validateExtraction(format, result); e != nil {
				return Extraction{}, e
			}
			return result, nil
		case <-ticker.C:
			rss, e := processRSS(command.Process.Pid)
			if e != nil {
				if errors.Is(e, unix.ESRCH) || errors.Is(e, os.ErrNotExist) {
					continue
				}
				reason = errors.New("Document worker memory monitoring unavailable")
			} else if rss > rssLimit {
				reason = errors.New("Document worker resident-memory threshold exceeded")
			}
			if reason != nil {
				_ = command.Process.Kill()
				<-done
				return result, reason
			}
		}
	}
}
func validateExtraction(format string, x Extraction) error {
	if x.Info.Format != format || x.Info.Engine != "filewise-go-documents-v1" || x.Info.Scope != "text_only; no OCR or visual layout" {
		return errors.New("Document worker format mismatch")
	}
	switch x.Info.Status {
	case "text", "partial", "no_text", "unsupported", "failed":
	default:
		return errors.New("Invalid document worker status")
	}
	if len(x.Fragments) > maxFragments || x.Info.FragmentCount != len(x.Fragments) || len(x.Info.Notes) > 1000 || len(x.Info.Error) > 8192 || !utf8.ValidString(x.Info.Error) {
		return errors.New("Invalid document worker bounds")
	}
	if (x.Info.Status == "failed" || x.Info.Status == "unsupported" || x.Info.Status == "no_text") && len(x.Fragments) > 0 {
		return errors.New("Failed extraction cannot be evidence")
	}
	if x.Info.Status == "failed" && len(x.Sheets) > 0 {
		return errors.New("Failed extraction cannot be table data")
	}
	total := 0
	for _, f := range x.Fragments {
		total += utf8.RuneCountInString(f.Text)
		if total > maxText || len(f.Locator) > 256 || f.Locator == "" || !utf8.ValidString(f.Text) {
			return errors.New("Invalid document worker fragments")
		}
	}
	for _, n := range x.Info.Notes {
		if len(n) > 512 || !utf8.ValidString(n) {
			return errors.New("Invalid document worker notes")
		}
	}
	if len(x.Sheets) > 100 || format != "xlsx" && len(x.Sheets) > 0 {
		return errors.New("Invalid document worker sheets")
	}
	cells := 0
	for _, sheet := range x.Sheets {
		if utf8.RuneCountInString(sheet.Name) > 100 {
			return errors.New("Invalid document worker sheet name")
		}
		for _, c := range sheet.Cells {
			cells++
			row, col, e := address(c.Address)
			if e != nil || row != c.Row || col != c.Column || cells > maxFragments {
				return errors.New("Invalid document worker cell")
			}
		}
	}
	return nil
}

// WorkerMain is a resource-isolated parser, not a filesystem/network sandbox.
// RSS sampling and Go's soft memory target are NOT a hard RSS guarantee. Linux
// additionally applies RLIMIT_DATA; CPU/core limits apply on both platforms.
func WorkerMain(format string) error {
	if !officeFormat(format) {
		return errors.New("Invalid document worker format")
	}
	for resource, limit := range map[int]uint64{unix.RLIMIT_CPU: 15, unix.RLIMIT_CORE: 0, unix.RLIMIT_NOFILE: 64} {
		if e := unix.Setrlimit(resource, &unix.Rlimit{Cur: limit, Max: limit}); e != nil {
			return fmt.Errorf("Document worker resource limit: %w", e)
		}
	}
	if e := workerMemoryLimit(); e != nil {
		return e
	}
	debug.SetMemoryLimit(256 << 20)
	body, e := io.ReadAll(io.LimitReader(os.Stdin, MaxFile+1))
	if e != nil || len(body) > MaxFile {
		return errors.New("Document worker input limit exceeded")
	}
	output := &boundedOutput{limit: workerOutput}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if e = encoder.Encode(extract("input."+format, body)); e != nil {
		return e
	}
	_, e = os.Stdout.Write(output.Bytes())
	return e
}
