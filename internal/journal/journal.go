//go:build darwin || linux

// Package journal captures one document's immutable originals and derived meta.
// It is a Go migration component, not the legacy workspace database or an Agent
// authorization boundary. Call it only as a local operator.
package journal

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/huaiwen/filewise/internal/changes"
	"github.com/huaiwen/filewise/internal/documents"
)

type Meta struct {
	SemanticChange changes.Metadata `json:"semantic_change"`
}
type Record struct {
	Schema     string `json:"schema"`
	Parent     string `json:"parent,omitempty"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	CapturedAt string `json:"captured_at"`
	Meta       Meta   `json:"meta"`
}
type Receipt struct {
	ID        string `json:"id"`
	Record    Record `json:"record"`
	Unchanged bool   `json:"unchanged"`
}
type Journal struct {
	root *os.Root
	lock *os.File
	Path string
}

var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ReadFile(name string) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > documents.MaxFile {
		return nil, errors.New("Expected a regular file no larger than 10 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(f, documents.MaxFile+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if len(body) > documents.MaxFile || int64(len(body)) != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !os.SameFile(before, current) || current.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("File changed while being read; retry when stable")
	}
	return body, nil
}
func Open(state, name string) (*Journal, error) {
	path, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return nil, err
	}
	if err = os.Mkdir(state, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(state)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Journal must be a private real directory (0700)")
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return nil, err
	}
	j := &Journal{root: root, Path: path}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		j.Close()
		return nil, errors.New("Journal directory changed")
	}
	expected, _ := json.Marshal(struct {
		Schema string `json:"schema"`
		Path   string `json:"path"`
	}{"filewise-go/document-journal-v1", path})
	existing, err := j.read("format.json", 16<<10)
	if err == nil && !bytes.Equal(existing, expected) {
		j.Close()
		return nil, errors.New("Journal schema or original path differs; implicit migration/rename is not supported")
	}
	if errors.Is(err, os.ErrNotExist) {
		dir, e := root.Open(".")
		if e != nil {
			j.Close()
			return nil, e
		}
		entries, e := dir.ReadDir(-1)
		dir.Close()
		if e != nil {
			j.Close()
			return nil, e
		}
		for _, entry := range entries {
			if entry.Name() != "lock" {
				j.Close()
				return nil, errors.New("Unrecognized journal; legacy state is not imported or overwritten")
			}
		}
	} else if err != nil {
		j.Close()
		return nil, err
	}
	j.lock, err = root.OpenFile("lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err == nil {
		err = private(j.lock)
	}
	if err == nil {
		err = syscall.Flock(int(j.lock.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		j.Close()
		return nil, err
	}
	existing, err = j.read("format.json", 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		dir, e := root.Open(".")
		if e != nil {
			j.Close()
			return nil, e
		}
		entries, e := dir.ReadDir(-1)
		dir.Close()
		if e != nil {
			j.Close()
			return nil, e
		}
		for _, entry := range entries {
			if entry.Name() != "lock" {
				j.Close()
				return nil, errors.New("Unrecognized journal; legacy state is not imported or overwritten")
			}
		}
		err = j.create("format.json", expected)
	} else if err == nil && !bytes.Equal(existing, expected) {
		err = errors.New("Journal schema or original path differs; implicit migration/rename is not supported")
	}
	if err == nil {
		for _, d := range []string{"blobs", "records"} {
			if e := root.Mkdir(d, 0700); e != nil && !errors.Is(e, os.ErrExist) {
				err = e
				break
			}
			s, e := root.Lstat(d)
			if e != nil {
				err = e
				break
			}
			if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 || s.Mode().Perm()&0077 != 0 {
				err = errors.New("Unsafe journal subdirectory")
				break
			}
		}
	}
	if err != nil {
		j.Close()
		return nil, err
	}
	return j, nil
}
func (j *Journal) Close() error {
	if j.lock != nil {
		j.lock.Close()
	}
	if j.root != nil {
		return j.root.Close()
	}
	return nil
}
func private(f *os.File) error {
	s, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0 || !ok || st.Nlink != 1 {
		return errors.New("Journal files must be private regular single-link files (0600)")
	}
	return nil
}
func (j *Journal) read(name string, limit int64) ([]byte, error) {
	f, err := j.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = private(f); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("Journal entry size limit exceeded")
	}
	return data, err
}
func (j *Journal) syncDir(name string) error {
	f, err := j.root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (j *Journal) create(name string, body []byte) error {
	temp := ".pending-" + rand.Text()
	f, err := j.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer j.root.Remove(temp)
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	err = j.root.Link(temp, name)
	if errors.Is(err, os.ErrExist) {
		old, e := j.read(name, int64(len(body)))
		if e != nil {
			return e
		}
		if !bytes.Equal(old, body) {
			return errors.New("Immutable journal object conflicts with stored bytes")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err = j.root.Remove(temp); err != nil {
		return err
	}
	return j.syncDir(filepath.Dir(name))
}
func (j *Journal) latest() (string, error) {
	body, err := j.read("latest", 64)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !digestRE.Match(body) {
		return "", errors.New("Invalid latest record ID")
	}
	return string(body), nil
}
func (j *Journal) record(id string) (Record, []byte, error) {
	var r Record
	if !digestRE.MatchString(id) {
		return r, nil, errors.New("Invalid record ID")
	}
	body, err := j.read("records/"+id+".json", 16<<20)
	if err != nil {
		return r, nil, err
	}
	if documents.Hash(body) != id {
		return r, nil, errors.New("Record integrity failed")
	}
	if err = json.Unmarshal(body, &r); err != nil {
		return r, nil, err
	}
	if r.Schema != "filewise-go/document-record-v1" || r.Path != j.Path || !digestRE.MatchString(r.SHA256) || r.Meta.SemanticChange.After.SHA256 != r.SHA256 || r.Meta.SemanticChange.Schema != "filewise-go/change-v1" {
		return r, nil, errors.New("Record/source binding failed")
	}
	if r.Parent != "" && !digestRE.MatchString(r.Parent) {
		return r, nil, errors.New("Invalid parent record")
	}
	if r.Parent == "" && r.Meta.SemanticChange.Before != nil {
		return r, nil, errors.New("Unexpected baseline evidence")
	}
	if r.Parent != "" {
		parentBody, e := j.read("records/"+r.Parent+".json", 16<<20)
		if e != nil {
			return r, nil, e
		}
		var parent Record
		if documents.Hash(parentBody) != r.Parent || json.Unmarshal(parentBody, &parent) != nil || parent.Schema != r.Schema || parent.Path != r.Path || !digestRE.MatchString(parent.SHA256) || r.Meta.SemanticChange.Before == nil || r.Meta.SemanticChange.Before.SHA256 != parent.SHA256 {
			return r, nil, errors.New("Baseline evidence binding failed")
		}
		before, e := j.read("blobs/"+parent.SHA256, documents.MaxFile)
		if e != nil {
			return r, nil, e
		}
		if documents.Hash(before) != parent.SHA256 {
			return r, nil, errors.New("Baseline original integrity failed")
		}
	}
	original, err := j.read("blobs/"+r.SHA256, documents.MaxFile)
	if err != nil {
		return r, nil, err
	}
	if documents.Hash(original) != r.SHA256 {
		return r, nil, errors.New("Original integrity failed")
	}
	return r, original, nil
}
func (j *Journal) Read(id string) (Receipt, error) {
	if id == "latest" {
		var err error
		id, err = j.latest()
		if err != nil {
			return Receipt{}, err
		}
	}
	r, _, err := j.record(id)
	return Receipt{ID: id, Record: r}, err
}

// Capture requires the exact previous record ID (or "none" initially). It never
// writes the original and never approves, publishes, or evaluates project gates.
func (j *Journal) Capture(base string) (Receipt, error) {
	latest, err := j.latest()
	if err != nil {
		return Receipt{}, err
	}
	if (latest == "" && base != "none") || (latest != "" && base != latest) {
		return Receipt{}, errors.New("STALE: provide the exact previous record ID, or none for the first capture")
	}
	var previous Record
	var before []byte
	if latest != "" {
		previous, before, err = j.record(latest)
		if err != nil {
			return Receipt{}, err
		}
	}
	after, err := ReadFile(j.Path)
	if err != nil {
		return Receipt{}, err
	}
	sha := documents.Hash(after)
	if latest != "" && sha == previous.SHA256 {
		return Receipt{ID: latest, Record: previous, Unchanged: true}, nil
	}
	meta := changes.Derive(filepath.Base(j.Path), before, after, latest != "")
	record := Record{Schema: "filewise-go/document-record-v1", Parent: latest, Path: j.Path, SHA256: sha, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Meta: Meta{meta}}
	// Re-read before committing the captured object. This is a completed file
	// capture, not a promise that the live original stays unchanged afterwards.
	again, err := ReadFile(j.Path)
	if err != nil {
		return Receipt{}, err
	}
	if !bytes.Equal(again, after) {
		return Receipt{}, errors.New("STALE: original changed during analysis")
	}
	body, err := json.Marshal(record)
	if err != nil {
		return Receipt{}, err
	}
	if len(body) > 16<<20 {
		return Receipt{}, errors.New("Record exceeds 16 MiB")
	}
	id := documents.Hash(body)
	if err = j.create("blobs/"+sha, after); err != nil {
		return Receipt{}, err
	}
	if err = j.create("records/"+id+".json", body); err != nil {
		return Receipt{}, err
	}
	// Under the cross-process lock, remove only our fixed interrupted pointer file.
	if _, e := j.root.Lstat("latest.pending"); e == nil {
		if _, e = j.read("latest.pending", 64); e != nil {
			return Receipt{}, e
		}
		if e = j.root.Remove("latest.pending"); e != nil {
			return Receipt{}, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return Receipt{}, e
	}
	if err = j.create("latest.pending", []byte(id)); err != nil {
		return Receipt{}, err
	}
	if err = j.root.Rename("latest.pending", "latest"); err != nil {
		return Receipt{}, err
	}
	if err = j.syncDir("."); err != nil {
		return Receipt{}, fmt.Errorf("Capture pointer changed; inspect latest before retry: %w", err)
	}
	return Receipt{ID: id, Record: record}, nil
}
