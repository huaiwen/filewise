//go:build darwin || linux

package workspace

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gobwas/glob"
	"golang.org/x/sys/unix"
)

const MaxFile = 10 << 20
const MaxTotal = 50 << 20

func Relative(path string) error {
	if e := bounded(path, 1, 240, "path"); e != nil {
		return e
	}
	if strings.ContainsAny(path, "\\\x00:") {
		return fail(422, "Path must be normalized and project-relative")
	}
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." || p == ".." {
			return fail(422, "Path must be normalized and project-relative")
		}
	}
	return nil
}
func globMatch(pattern, path string) bool {
	g, e := glob.Compile(pattern)
	return e == nil && g.Match(path)
}
func Excluded(path string, spec M) bool {
	fixed := []string{".git", ".filewise", ".filewise-rust", ".filewise-go", ".codex", ".claude", ".agents", ".pi", ".venv", "node_modules", "target", "__pycache__", ".ds_store", ".env", ".env.*", "*tokens*.json", "*.pem", "*.key", "*.db", "*.db-*", "*.sqlite*", "*.p12", "*.pfx", "id_rsa", "id_ed25519", ".ssh", ".aws", ".filewise-write-*"}
	match := func(p, path string) bool {
		if globMatch(p, path) {
			return true
		}
		for _, part := range strings.Split(path, "/") {
			if globMatch(p, part) {
				return true
			}
		}
		return false
	}
	for _, p := range fixed {
		if match(p, strings.ToLower(path)) {
			return true
		}
	}
	for _, p := range strs(spec["excludes"]) {
		if match(p, path) {
			return true
		}
	}
	return false
}
func Included(path string, spec M) bool {
	for _, p := range strs(spec["includes"]) {
		if globMatch(p, path) || strings.HasPrefix(p, "**/") && globMatch(p[3:], path) {
			return true
		}
	}
	return false
}
func directory(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fail(422, "Normalized absolute directory required")
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if create && errors.Is(err, unix.ENOENT) {
			err = unix.Mkdirat(fd, part, 0700)
			if err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}
func readAt(dir *os.File, name string) ([]byte, os.FileInfo, error) {
	fd, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(e, unix.ENOENT) {
		return nil, nil, nil
	}
	if e != nil {
		return nil, nil, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return nil, nil, e
	}
	if !before.Mode().IsRegular() || before.Size() > MaxFile {
		return nil, nil, fail(413, "Not a bounded regular file")
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if e != nil {
		return nil, nil, e
	}
	if len(b) > MaxFile {
		return nil, nil, fail(413, "File exceeds 10 MiB")
	}
	after, e := f.Stat()
	if e != nil {
		return nil, nil, e
	}
	var current unix.Stat_t
	e = unix.Fstatat(int(dir.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW)
	if e != nil {
		return nil, nil, e
	}
	var original unix.Stat_t
	if e = unix.Fstat(fd, &original); e != nil {
		return nil, nil, e
	}
	if before.Size() != int64(len(b)) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || current.Dev != original.Dev || current.Ino != original.Ino || current.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, nil, fail(409, "File changed while being read; retry when stable")
	}
	return b, after, nil
}
func ReadFile(root, path string) ([]byte, bool, error) {
	if e := Relative(path); e != nil {
		return nil, false, e
	}
	full := filepath.Join(root, path)
	d, e := directory(filepath.Dir(full), false)
	if errors.Is(e, unix.ENOENT) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	defer d.Close()
	b, info, e := readAt(d, filepath.Base(full))
	return b, info != nil, e
}
func originalHash(b []byte, exists bool) string {
	if !exists {
		return ""
	}
	return Hash(b)
}
func appliedError(err error) error {
	if err == nil {
		return nil
	}
	return &Error{Status: 409, Message: err.Error(), Applied: true}
}
func wasApplied(err error) bool { var e *Error; return errors.As(err, &e) && e.Applied }
func Replace(root, path string, body []byte, exists bool, expected string) error {
	if e := Relative(path); e != nil {
		return e
	}
	r, e := directory(root, false)
	if e != nil {
		return e
	}
	r.Close()
	full := filepath.Join(root, path)
	d, e := directory(filepath.Dir(full), exists)
	if e != nil {
		return e
	}
	defer d.Close()
	name := filepath.Base(full)
	old, info, e := readAt(d, name)
	if e != nil {
		return e
	}
	if originalHash(old, info != nil) != expected {
		return fail(409, "Original changed before writeback: "+path)
	}
	if !exists {
		if info == nil {
			return nil
		}
		if e = unix.Unlinkat(int(d.Fd()), name, 0); e != nil {
			return e
		}
		return appliedError(d.Sync())
	}
	if len(body) > MaxFile {
		return fail(413, "File exceeds 10 MiB")
	}
	temp := ".filewise-write-" + randomID()
	mode := os.FileMode(0600)
	if info != nil {
		mode = info.Mode().Perm()
	}
	fd, e := unix.Openat(int(d.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode))
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), temp)
	defer f.Close()
	defer unix.Unlinkat(int(d.Fd()), temp, 0)
	if _, e = f.Write(body); e != nil {
		return e
	}
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	old, current, e := readAt(d, name)
	if e != nil {
		return e
	}
	if originalHash(old, current != nil) != expected {
		return fail(409, "Original changed during writeback")
	}
	// ponytail: byte CAS requires exclusive filesystem ownership to distinguish
	// an identical external write in the final check-to-rename interval.
	if expected == "" {
		e = unix.Linkat(int(d.Fd()), temp, int(d.Fd()), name, 0)
	} else {
		e = unix.Renameat(int(d.Fd()), temp, int(d.Fd()), name)
	}
	if e != nil {
		return e
	}
	return appliedError(d.Sync())
}
func Move(root, from, to, expected string) error {
	if e := Relative(from); e != nil {
		return e
	}
	if e := Relative(to); e != nil {
		return e
	}
	src, dst := filepath.Join(root, from), filepath.Join(root, to)
	a, e := directory(filepath.Dir(src), false)
	if e != nil {
		return e
	}
	defer a.Close()
	b, e := directory(filepath.Dir(dst), true)
	if e != nil {
		return e
	}
	defer b.Close()
	src, dst = filepath.Base(src), filepath.Base(dst)
	old, info, e := readAt(a, src)
	if e != nil {
		return e
	}
	_, target, e := readAt(b, dst)
	if e != nil {
		return e
	}
	if originalHash(old, info != nil) != expected || target != nil {
		return fail(409, "Move conflict; original or destination changed")
	}
	if e = unix.Linkat(int(a.Fd()), src, int(b.Fd()), dst, 0); e != nil {
		return e
	}
	finish := func() error {
		var x, y unix.Stat_t
		if e := unix.Fstatat(int(a.Fd()), src, &x, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return e
		}
		if e := unix.Fstatat(int(b.Fd()), dst, &y, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return e
		}
		aa, ai, e := readAt(a, src)
		if e != nil {
			return e
		}
		bb, bi, e := readAt(b, dst)
		if e != nil {
			return e
		}
		if x.Dev != y.Dev || x.Ino != y.Ino || originalHash(aa, ai != nil) != expected || originalHash(bb, bi != nil) != expected {
			return fail(409, "Move conflict; preserve external edits")
		}
		if e = b.Sync(); e != nil {
			return e
		}
		if e = unix.Unlinkat(int(a.Fd()), src, 0); e != nil {
			return e
		}
		return a.Sync()
	}
	return appliedError(finish())
}
func Scan(root string, spec M) (map[string][]byte, error) {
	d, e := directory(root, false)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	files := map[string][]byte{}
	total := 0
	var visit func(*os.File, string, int) error
	visit = func(dir *os.File, prefix string, depth int) error {
		if depth > 128 {
			return fail(413, "Project directory depth exceeds 128")
		}
		entries, e := dir.ReadDir(-1)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			name := entry.Name()
			path := prefix + name
			if !utf8.ValidString(path) {
				return fail(422, "Paths must be UTF-8")
			}
			if Excluded(path, spec) || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				fd, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if e != nil {
					return e
				}
				next := os.NewFile(uintptr(fd), path)
				e = visit(next, path+"/", depth+1)
				next.Close()
				if e != nil {
					return e
				}
				continue
			}
			if !entry.Type().IsRegular() || !Included(path, spec) {
				continue
			}
			if e := Relative(path); e != nil {
				return e
			}
			b, info, e := readAt(dir, name)
			if e != nil {
				return e
			}
			if info == nil {
				return fail(409, "File disappeared during scan")
			}
			total += len(b)
			if total > MaxTotal || len(files) >= 1000 {
				return fail(413, "Project exceeds 1,000 files or 50 MiB")
			}
			files[path] = b
		}
		return nil
	}
	if e = visit(d, "", 0); e != nil {
		return nil, e
	}
	return files, nil
}
func privateFile(path string, create bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT
	}
	fd, e := unix.Open(path, flags, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	info, e := f.Stat()
	var stat unix.Stat_t
	if e == nil {
		e = unix.Fstat(fd, &stat)
	}
	if e != nil {
		f.Close()
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Nlink != 1 {
		f.Close()
		return nil, fail(403, "State files must be private regular single-link files (0600)")
	}
	return f, nil
}
func privatePath(path string) (string, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	parent := filepath.Dir(abs)
	if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fail(403, "State directory must not be a symlink")
	}
	ancestor := parent
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			rel, err := filepath.Rel(ancestor, parent)
			if err != nil {
				return "", err
			}
			parent = filepath.Join(resolved, rel)
			abs = filepath.Join(parent, filepath.Base(abs))
			break
		}
		if !errors.Is(err, os.ErrNotExist) || ancestor == filepath.Dir(ancestor) {
			return "", err
		}
		ancestor = filepath.Dir(ancestor)
	}
	d, e := directory(parent, true)
	if e != nil {
		return "", e
	}
	defer d.Close()
	info, e := d.Stat()
	if e != nil {
		return "", e
	}
	if info.Mode().Perm()&0022 != 0 {
		return "", fail(403, "State directory must not be writable by group or others")
	}
	return abs, nil
}
func AuthInit(path string) (M, error) {
	path, e := privatePath(path)
	if e != nil {
		return nil, e
	}
	t := Tokens{}
	for _, role := range []string{"reader", "editor", "reviewer", "publisher"} {
		t[randomID()] = Actor{ID: "local-" + role, Roles: []string{role}, Audience: "operator", WorkspaceProjects: []string{}}
	}
	d, e := directory(filepath.Dir(path), false)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	fd, e := unix.Openat(int(d.Fd()), filepath.Base(path), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if _, e = f.Write(JSON(t)); e != nil {
		return nil, e
	}
	if e = f.Sync(); e != nil {
		return nil, e
	}
	if e = d.Sync(); e != nil {
		return nil, e
	}
	return M{"tokens_file": path, "identities": len(t)}, nil
}
func ReadTokens(path string) (Tokens, error) {
	f, e := privateFile(path, false)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if e != nil {
		return nil, e
	}
	if len(b) > MaxFile {
		return nil, fail(413, "Credential file exceeds limit")
	}
	t := Tokens{}
	if e = Decode(b, &t); e != nil {
		return nil, e
	}
	for k, a := range t {
		if a.Audience == "" {
			a.Audience = "operator"
			t[k] = a
		}
	}
	if e = ValidateTokens(t); e != nil {
		return nil, e
	}
	return t, nil
}
