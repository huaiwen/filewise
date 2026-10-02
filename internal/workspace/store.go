//go:build darwin || linux

package workspace

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/huaiwen/filewise/internal/changes"
	"github.com/huaiwen/filewise/internal/documents"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const schemaSQL = `PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS native_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT OR IGNORE INTO native_meta VALUES('schema','filewise-go-v1');
CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY,spec TEXT NOT NULL,root TEXT,active TEXT);
CREATE TABLE IF NOT EXISTS blobs(sha TEXT PRIMARY KEY,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS sources(id TEXT PRIMARY KEY,project TEXT NOT NULL REFERENCES projects(id),path TEXT NOT NULL,sha TEXT NOT NULL REFERENCES blobs(sha),acl TEXT NOT NULL,revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS versions(id TEXT PRIMARY KEY,project TEXT NOT NULL REFERENCES projects(id),bundle TEXT NOT NULL,status TEXT NOT NULL,available_at TEXT,availability_hash TEXT,approver TEXT,revoked INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS version_project ON versions(project,available_at);
CREATE TABLE IF NOT EXISTS operations(project TEXT,actor TEXT,request TEXT,request_digest TEXT NOT NULL,version TEXT NOT NULL REFERENCES versions(id),PRIMARY KEY(project,actor,request));
CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,project TEXT NOT NULL,actor TEXT NOT NULL,bundle TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS audit(seq INTEGER PRIMARY KEY,project TEXT NOT NULL,event TEXT NOT NULL,previous TEXT NOT NULL,hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS watches(project TEXT PRIMARY KEY REFERENCES projects(id),bundle TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS watch_jobs(id TEXT PRIMARY KEY,project TEXT NOT NULL REFERENCES projects(id),status TEXT NOT NULL,bundle TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS watch_queue ON watch_jobs(status);`

type Store struct {
	DB   *sql.DB
	Path string
	lock *os.File
	Mu   sync.Mutex
}

func Open(path string) (*Store, error) {
	path, e := privatePath(path)
	if e != nil {
		return nil, e
	}
	lock, e := privateFile(path+".lock", true)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX); e != nil {
		lock.Close()
		return nil, e
	}
	s := &Store{Path: path, lock: lock}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	file, e := privateFile(path, true)
	if e != nil {
		return nil, e
	}
	defer file.Close()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		f, e := privateFile(path+suffix, false)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		if f != nil {
			f.Close()
		}
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	s.DB = db
	db.SetMaxOpenConns(1)
	if _, e = db.Exec("PRAGMA busy_timeout=10000;"); e != nil {
		return nil, e
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&n); e != nil {
		return nil, e
	}
	if n > 0 {
		var schema string
		if e = db.QueryRow("SELECT value FROM native_meta WHERE key='schema'").Scan(&schema); e != nil || schema != "filewise-go-v1" {
			return nil, fail(409, "Legacy or unsupported database: use a separate Go database; migration is not implicit")
		}
	}
	if _, e = db.Exec(schemaSQL); e != nil {
		return nil, e
	}
	a, e := file.Stat()
	if e != nil {
		return nil, e
	}
	b, e := os.Lstat(path)
	if e != nil || b.Mode()&os.ModeSymlink != 0 || !os.SameFile(a, b) {
		return nil, fail(409, "Database path changed")
	}
	ok = true
	return s, nil
}

// Lock serializes a service operation with other processes using this database.
// Model inference releases this lock and revalidates before applying its result.
func (s *Store) Lock() error {
	s.Mu.Lock()
	if e := unix.Flock(int(s.lock.Fd()), unix.LOCK_EX); e != nil {
		s.Mu.Unlock()
		return e
	}
	return nil
}
func (s *Store) Unlock() { _ = unix.Flock(int(s.lock.Fd()), unix.LOCK_UN); s.Mu.Unlock() }
func (s *Store) Close() error {
	var e error
	if s.DB != nil {
		e = s.DB.Close()
	}
	if s.lock != nil {
		unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
		s.lock.Close()
	}
	return e
}
func (s *Store) transaction(fn func(*sql.Tx) error) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func audit(tx *sql.Tx, project string, actor Actor, action string, data any) (M, error) {
	previous := ""
	e := tx.QueryRow("SELECT hash FROM audit ORDER BY seq DESC LIMIT 1").Scan(&previous)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	event := M{"project_id": project, "actor": actor.ID, "action": action, "recorded_at": now(), "data": data}
	signature := digest(M{"previous": previous, "event": event})
	_, e = tx.Exec("INSERT INTO audit(project,event,previous,hash) VALUES(?,?,?,?)", project, string(JSON(event)), previous, signature)
	return M{"id": signature, "actor": actor.ID, "action": action, "data": data}, e
}
func (s *Store) Receipt(project string, actor Actor, action string, data any) (M, error) {
	var result M
	e := s.transaction(func(tx *sql.Tx) error { var e error; result, e = audit(tx, project, actor, action, data); return e })
	return result, e
}
func (s *Store) Project(project string, actor Actor) (M, string, *string, error) {
	if e := actor.Access(project); e != nil {
		return nil, "", nil, e
	}
	var body string
	var root, active *string
	e := s.DB.QueryRow("SELECT spec,root,active FROM projects WHERE id=?", project).Scan(&body, &root, &active)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, "", nil, fail(404, "Project not found")
	}
	if e != nil {
		return nil, "", nil, e
	}
	v, e := StrictJSON([]byte(body))
	if e != nil {
		return nil, "", nil, e
	}
	spec, e := Spec(obj(v))
	if e != nil {
		return nil, "", nil, e
	}
	r := ""
	if root != nil {
		r = *root
	}
	return spec, r, active, nil
}
func (s *Store) Projects(actor Actor) ([]any, error) {
	if e := actor.Validate(); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query("SELECT id,spec,active FROM projects ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []any{}
	for rows.Next() {
		var id, body string
		var active *string
		if e = rows.Scan(&id, &body, &active); e != nil {
			return nil, e
		}
		if actor.Access(id) == nil {
			v, e := StrictJSON([]byte(body))
			if e != nil {
				return nil, e
			}
			result = append(result, M{"id": id, "name": obj(v)["name"], "active_release": active})
		}
	}
	return result, rows.Err()
}
func (s *Store) Create(input M, root string, actor Actor) (M, error) {
	if e := actor.Operator("editor"); e != nil {
		return nil, e
	}
	spec, e := Spec(input)
	if e != nil {
		return nil, e
	}
	if root == "" {
		return nil, fail(501, "Upload projects are not implemented; register a local directory")
	}
	root, e = filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return nil, e
	}
	if within(root, s.Path) {
		return nil, fail(422, "Choose a directory separate from the private database")
	}
	if _, e = Scan(root, spec); e != nil {
		return nil, e
	}
	id := str(spec["id"])
	e = s.transaction(func(tx *sql.Tx) error {
		var n int
		if e := tx.QueryRow("SELECT count(*) FROM projects WHERE id=?", id).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return fail(409, "Project already exists")
		}
		if _, e := tx.Exec("INSERT INTO projects(id,spec,root) VALUES(?,?,?)", id, string(JSON(spec)), root); e != nil {
			return e
		}
		_, e := audit(tx, id, actor, "project.created", M{"spec": spec})
		return e
	})
	return M{"id": id, "name": spec["name"], "root": root}, e
}
func within(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}
func (s *Store) Source(id, project string, actor Actor) (string, string, []byte, error) {
	if e := actor.Access(project); e != nil {
		return "", "", nil, e
	}
	var path, sha, acl string
	var revoked bool
	var body []byte
	e := s.DB.QueryRow("SELECT s.path,s.sha,s.acl,s.revoked,b.body FROM sources s JOIN blobs b ON s.sha=b.sha WHERE s.id=? AND s.project=?", id, project).Scan(&path, &sha, &acl, &revoked, &body)
	if errors.Is(e, sql.ErrNoRows) {
		return "", "", nil, fail(404, "Source not found")
	}
	if e != nil {
		return "", "", nil, e
	}
	var roles []string
	if e = Decode([]byte(acl), &roles); e != nil {
		return "", "", nil, e
	}
	allowed := false
	for _, r := range actor.Roles {
		if slices.Contains(roles, r) {
			allowed = true
		}
	}
	if revoked || !allowed {
		return "", "", nil, fail(403, "Source revoked or access denied")
	}
	if Hash(body) != sha || digest(M{"project": project, "path": path, "sha256": sha}) != id {
		return "", "", nil, fail(409, "Source integrity failed")
	}
	return path, sha, body, nil
}
func (s *Store) Evidence(project string, evidence M, actor Actor) error {
	path, sha, b, e := s.Source(str(evidence["source_id"]), project, actor)
	if e != nil {
		return e
	}
	quote, loc := str(evidence["quote"]), str(evidence["locator"])
	valid := quote != "" && loc == "file:sha256" && quote == sha
	if quote != "" && loc != "file:sha256" {
		for _, f := range documents.Extract(path, b).Fragments {
			if f.Locator == loc && strings.Contains(f.Text, quote) {
				valid = true
				break
			}
		}
	}
	if !valid {
		return fail(409, "Evidence anchor does not match source")
	}
	return nil
}
func (s *Store) Policy(project, source string, acl []string, revoked bool, actor Actor) (M, error) {
	if e := actor.Operator("reviewer"); e != nil {
		return nil, e
	}
	if _, _, _, e := s.Project(project, actor); e != nil {
		return nil, e
	}
	if len(acl) == 0 {
		return nil, fail(422, "ACL cannot be empty")
	}
	for _, r := range acl {
		if !slices.Contains([]string{"reader", "editor", "reviewer", "publisher"}, r) {
			return nil, fail(422, "Invalid ACL role")
		}
	}
	acl = unique(acl)
	result := M{"source_id": source, "acl": acl, "revoked": revoked}
	e := s.transaction(func(tx *sql.Tx) error {
		r, e := tx.Exec("UPDATE sources SET acl=?,revoked=? WHERE project=? AND id=?", string(JSON(acl)), revoked, project, source)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return fail(404, "Source not found")
		}
		_, e = audit(tx, project, actor, "source.policy", result)
		return e
	})
	return result, e
}
func (s *Store) rawVersion(project, id string, actor Actor) (*Version, error) {
	if e := actor.Access(project); e != nil {
		return nil, e
	}
	if e := Exact(id); e != nil {
		return nil, e
	}
	v := &Version{ID: id}
	var body string
	var signature *string
	e := s.DB.QueryRow("SELECT bundle,status,available_at,availability_hash,approver,revoked FROM versions WHERE project=? AND id=?", project, id).Scan(&body, &v.Status, &v.AvailableAt, &signature, &v.Approver, &v.Revoked)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, fail(404, "Version not found")
	}
	if e != nil {
		return nil, e
	}
	value, e := StrictJSON([]byte(body))
	if e != nil {
		return nil, e
	}
	v.Snapshot = obj(value)
	if v.Snapshot["schema"] != "filewise-go/snapshot-v1" || v.Snapshot["project_id"] != project || Hash([]byte(body)) != id {
		return nil, fail(409, "Snapshot integrity failed")
	}
	if v.Revoked {
		return nil, fail(409, "Version is revoked")
	}
	if (v.Status == "completed") != (v.AvailableAt != nil) {
		return nil, fail(409, "Availability integrity failed")
	}
	if v.AvailableAt != nil && (signature == nil || *signature != digest(M{"version": id, "project_id": project, "available_at": *v.AvailableAt})) {
		return nil, fail(409, "Availability integrity failed")
	}
	for p, raw := range v.Files() {
		f := obj(raw)
		if Relative(p) != nil || f["path"] != p || digest(M{"project": project, "path": p, "sha256": f["sha256"]}) != f["source_id"] {
			return nil, fail(409, "File/source binding failed")
		}
		path, sha, b, e := s.Source(str(f["source_id"]), project, actor)
		if e != nil {
			return nil, e
		}
		if path != p || sha != f["sha256"] || len(b) != integer(f["size"]) {
			return nil, fail(409, "File integrity failed")
		}
		for _, anchor := range list(obj(f["metadata"])["evidence"]) {
			if e = s.Evidence(project, obj(anchor), actor); e != nil {
				return nil, e
			}
		}
	}
	return v, nil
}
func (s *Store) Version(project, selector string, asof any, actor Actor) (*Version, error) {
	_, _, active, e := s.Project(project, actor)
	if e != nil {
		return nil, e
	}
	var cut any
	if asof != nil {
		t, e := Timestamp(str(asof))
		if e != nil {
			return nil, e
		}
		if t > now() {
			return nil, fail(422, "as_of cannot be in the future")
		}
		cut = t
	}
	id := selector
	switch selector {
	case "latest":
		e = s.DB.QueryRow("SELECT id FROM versions WHERE project=? AND status='completed' AND (? IS NULL OR available_at<=?) ORDER BY available_at DESC,rowid DESC LIMIT 1", project, cut, cut).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, fail(404, "No completed snapshot at this time")
		}
		if e != nil {
			return nil, e
		}
	case "published":
		if active == nil {
			return nil, fail(404, "No published version")
		}
		id = *active
	case "HEAD":
		return nil, fail(501, "Named commits are not implemented")
	}
	v, e := s.rawVersion(project, id, actor)
	if e != nil {
		return nil, e
	}
	if cut != nil && (v.AvailableAt == nil || *v.AvailableAt > str(cut)) {
		return nil, fail(409, "Version was not available at as_of")
	}
	_, e = s.Lineage(project, v.Files(), cut, actor)
	if e != nil {
		return nil, e
	}
	return v, nil
}
func (s *Store) Lineage(project string, files M, cut any, actor Actor) ([]any, error) {
	queue := []any{}
	for _, path := range keys(files) {
		queue = append(queue, list(obj(files[path])["lineage"])...)
	}
	seen := map[string]bool{}
	versions := map[string]*Version{}
	result := []any{}
	for len(queue) > 0 {
		item := obj(queue[len(queue)-1])
		queue = queue[:len(queue)-1]
		version, path, role := str(item["version"]), str(item["path"]), str(item["role"])
		if Exact(version) != nil || Relative(path) != nil || !slices.Contains([]string{"data", "mapping", "code", "model", "baseline"}, role) {
			return nil, fail(409, "Invalid lineage")
		}
		key := digest([]string{version, path, role})
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(seen) > 1000 {
			return nil, fail(413, "Lineage exceeds 1,000 inputs")
		}
		input := versions[version]
		if input == nil {
			var e error
			input, e = s.rawVersion(project, version, actor)
			if e != nil {
				return nil, e
			}
			versions[version] = input
		}
		file := input.File(path)
		if len(file) == 0 {
			return nil, fail(409, "Input path missing")
		}
		if input.AvailableAt == nil || str(item["available_at"]) != *input.AvailableAt || item["sha256"] != file["sha256"] || item["source_id"] != file["source_id"] {
			return nil, fail(409, "Input binding failed")
		}
		if cut != nil && *input.AvailableAt > str(cut) {
			return nil, fail(409, "Input is newer than the historical cutoff")
		}
		record := clone(item)
		meta := obj(file["metadata"])
		record["metadata_current"] = file["metadata_current"]
		record["processing"] = "source"
		if meta["processing"] != nil {
			record["processing"] = meta["processing"]
		}
		record["quality"] = file["data_quality"]
		record["inputs_declared"] = len(list(meta["lineage"])) > 0
		record["valid_from"], record["valid_until"] = meta["valid_from"], meta["valid_until"]
		result = append(result, record)
		queue = append(queue, list(file["lineage"])...)
	}
	return result, nil
}
func (s *Store) latest(project string, actor Actor) (*Version, error) {
	var n int
	if e := s.DB.QueryRow("SELECT count(*) FROM versions WHERE project=? AND status='completed'", project).Scan(&n); e != nil {
		return nil, e
	}
	if n == 0 {
		return nil, nil
	}
	return s.Version(project, "latest", nil, actor)
}
func (s *Store) recovered(project string) error {
	var n int
	if e := s.DB.QueryRow("SELECT count(*) FROM versions WHERE project=? AND status IN ('applying','recovery_required')", project).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return fail(409, "RECOVERY_REQUIRED: recover interrupted writes first")
	}
	return nil
}
func matches(root string, spec M, v *Version) error {
	bodies, e := Scan(root, spec)
	if e != nil {
		return e
	}
	if len(bodies) != len(v.Files()) {
		return fail(409, "STALE: originals changed; sync or inspect before saving")
	}
	for p, b := range bodies {
		if v.File(p)["sha256"] != Hash(b) {
			return fail(409, "STALE: originals changed; sync or inspect before saving")
		}
	}
	return nil
}
func (s *Store) prepare(spec M, before *Version, bodies map[string][]byte, metas M, message string, actor Actor) (*Version, error) {
	if len(bodies) > 1000 {
		return nil, fail(413, "Project size limit exceeded")
	}
	total := 0
	for _, b := range bodies {
		total += len(b)
	}
	if total > MaxTotal {
		return nil, fail(413, "Project size limit exceeded")
	}
	project := str(spec["id"])
	manifest := M{}
	paths := []string{}
	for p := range bodies {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		body := bodies[p]
		if len(body) > MaxFile || Excluded(p, spec) || !Included(p, spec) || Relative(p) != nil {
			return nil, fail(422, "File exceeds the registered project boundary")
		}
		sha := Hash(body)
		source := digest(M{"project": project, "path": p, "sha256": sha})
		old := M{}
		if before != nil {
			old = before.File(p)
		}
		meta, metaSHA, declared := old["metadata"], old["metadata_sha256"], old["declared_by"]
		if m, ok := metas[p]; ok {
			meta = m
			metaSHA = sha
			declared = actor.ID
		}
		current := meta == nil || metaSHA == sha
		contract := obj(spec["data_contracts"])[p]
		if contract == nil {
			contract = obj(meta)["data"]
		}
		refs := append([]any{}, list(obj(meta)["lineage"])...)
		var quality any
		if contract != nil {
			hasDelta := false
			for _, c := range list(obj(contract)["checks"]) {
				if obj(c)["op"] == "distinct_count_change" {
					hasDelta = true
				}
			}
			var baseline any
			if hasDelta && len(old) > 0 {
				baseline = before.ID
			}
			if hasDelta && old["sha256"] == sha && equal(old["data_contract"], contract) {
				baseline = obj(old["data_quality"])["baseline"]
			}
			var previous []byte
			if baseline != nil {
				prior, e := s.Version(project, str(baseline), nil, actor)
				if e != nil {
					return nil, e
				}
				if f := prior.File(p); len(f) > 0 {
					refs = append(refs, M{"version": baseline, "path": p, "role": "baseline"})
					_, _, previous, e = s.Source(str(f["source_id"]), project, actor)
					if e != nil {
						return nil, e
					}
				}
			}
			report := Quality(p, body, obj(contract), previous, baseline)
			if obj(meta)["data"] != nil && (!current || obj(spec["data_contracts"])[p] != nil && !equal(obj(meta)["data"], contract)) {
				report["decision"] = "BLOCKED"
				report["issues"] = append(list(report["issues"]), M{"reason": "stale_or_mismatched_data_contract"})
			}
			quality = report
		}
		lineage := []any{}
		for _, raw := range refs {
			item := clone(obj(raw))
			input, e := s.Version(project, str(item["version"]), nil, actor)
			if e != nil {
				return nil, e
			}
			f := input.File(str(item["path"]))
			if len(f) == 0 {
				return nil, fail(422, "Input path missing")
			}
			if input.AvailableAt == nil {
				return nil, fail(409, "Input must be a completed snapshot")
			}
			item["sha256"], item["source_id"], item["available_at"] = f["sha256"], f["source_id"], *input.AvailableAt
			lineage = append(lineage, item)
		}
		for _, anchor := range list(obj(meta)["evidence"]) {
			if e := s.Evidence(project, obj(anchor), actor); e != nil {
				return nil, e
			}
		}
		deps := unique(append(strs(obj(spec["dependencies"])[p]), strs(obj(meta)["depends_on"])...))
		e := s.transaction(func(tx *sql.Tx) error {
			if _, e := tx.Exec("INSERT OR IGNORE INTO blobs VALUES(?,?)", sha, body); e != nil {
				return e
			}
			_, e := tx.Exec("INSERT OR IGNORE INTO sources(id,project,path,sha,acl) VALUES(?,?,?,?,?)", source, project, p, sha, string(JSON([]string{"reader", "editor", "reviewer", "publisher"})))
			return e
		})
		if e != nil {
			return nil, e
		}
		if _, _, _, e = s.Source(source, project, actor); e != nil {
			return nil, e
		}
		derived := old["meta"]
		if old["sha256"] != sha || derived == nil {
			var previous []byte
			if len(old) > 0 {
				_, _, previous, e = s.Source(str(old["source_id"]), project, actor)
				if e != nil {
					return nil, e
				}
			}
			derived = M{"semantic_change": changes.Derive(p, previous, body, len(old) > 0)}
		}
		manifest[p] = M{"path": p, "sha256": sha, "source_id": source, "size": len(body), "metadata": meta, "metadata_current": current, "declared_by": declared, "metadata_sha256": metaSHA, "depends_on": deps, "data_contract": contract, "data_quality": quality, "lineage": lineage, "meta": derived}
	}
	verification := BuildChecks(spec, manifest, bodies)
	inputs, e := s.Lineage(project, manifest, nil, actor)
	if e != nil {
		return nil, e
	}
	issues, warnings := inputGuard(inputs, nil)
	verification["issues"] = append(list(verification["issues"]), issues...)
	verification["warnings"] = warnings
	if len(issues) > 0 {
		verification["decision"] = "BLOCKED"
	} else if len(warnings) > 0 && verification["decision"] == "PASS" {
		verification["decision"] = "NEEDS_REVIEW"
	}
	var base any
	if before != nil {
		base = before.ID
	}
	snapshot := M{"schema": "filewise-go/snapshot-v1", "project_id": project, "base_version": base, "author": actor.ID, "created_at": now(), "message": message, "files": manifest, "verification": verification}
	if len(JSON(snapshot)) > 64<<20 {
		return nil, fail(413, "Snapshot metadata exceeds 64 MiB")
	}
	return &Version{ID: digest(snapshot), Snapshot: snapshot}, nil
}
func complete(tx *sql.Tx, project, id string, actor Actor) error {
	instant := now()
	var last *string
	if e := tx.QueryRow("SELECT max(available_at) FROM versions WHERE project=?", project).Scan(&last); e != nil {
		return e
	}
	if last != nil && *last > instant {
		return fail(409, "Server clock moved backwards")
	}
	receipt := M{"version": id, "project_id": project, "available_at": instant}
	r, e := tx.Exec("UPDATE versions SET status='completed',available_at=?,availability_hash=? WHERE id=? AND available_at IS NULL", instant, digest(receipt), id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fail(409, "Snapshot completion conflict")
	}
	_, e = audit(tx, project, actor, "snapshot.available", receipt)
	return e
}
func (s *Store) Sync(project string, actor Actor) (M, error) {
	if e := actor.Require("editor"); e != nil {
		return nil, e
	}
	spec, root, _, e := s.Project(project, actor)
	if e != nil {
		return nil, e
	}
	if e = s.recovered(project); e != nil {
		return nil, e
	}
	before, e := s.latest(project, actor)
	if e != nil {
		return nil, e
	}
	bodies, e := Scan(root, spec)
	if e != nil {
		return nil, e
	}
	v, e := s.prepare(spec, before, bodies, M{}, "Capture working files", actor)
	if e != nil {
		return nil, e
	}
	if e = matches(root, spec, v); e != nil {
		return nil, e
	}
	e = s.transaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec("INSERT INTO versions(id,project,bundle,status) VALUES(?,?,?,'pending')", v.ID, project, string(JSON(v.Snapshot))); e != nil {
			return e
		}
		return complete(tx, project, v.ID, actor)
	})
	if e != nil {
		return nil, e
	}
	q, _ := Query(M{})
	return s.List(project, q, actor)
}
func (s *Store) Write(project string, input M, actor Actor) (M, error) {
	if e := actor.Require("editor"); e != nil {
		return nil, e
	}
	q, e := WriteQuery(input)
	if e != nil {
		return nil, e
	}
	spec, root, _, e := s.Project(project, actor)
	if e != nil {
		return nil, e
	}
	requestDigest := digest(q)
	var oldDigest, id string
	e = s.DB.QueryRow("SELECT request_digest,version FROM operations WHERE project=? AND actor=? AND request=?", project, actor.ID, q["request_id"]).Scan(&oldDigest, &id)
	if e == nil {
		if oldDigest != requestDigest {
			return nil, fail(409, "Idempotency key already used with different content")
		}
		v, e := s.Version(project, id, nil, actor)
		if e != nil {
			return nil, e
		}
		if v.Status != "completed" && v.Status != "candidate" {
			if e = s.apply(project, id, actor); e != nil {
				return nil, e
			}
		}
		return s.writeResult(project, id, actor)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if e = s.recovered(project); e != nil {
		return nil, e
	}
	before, e := s.Version(project, "latest", nil, actor)
	if e != nil {
		return nil, e
	}
	if before.ID != q["base_version"] {
		return nil, fail(409, "STALE: latest version changed")
	}
	if e = matches(root, spec, before); e != nil {
		return nil, e
	}
	bodies := map[string][]byte{}
	metas := M{}
	for p, f := range before.Files() {
		_, _, b, e := s.Source(str(obj(f)["source_id"]), project, actor)
		if e != nil {
			return nil, e
		}
		bodies[p] = b
	}
	for p, raw := range obj(q["changes"]) {
		c := obj(raw)
		if Excluded(p, spec) || !Included(p, spec) {
			return nil, fail(403, "Path is outside the managed boundary")
		}
		if len(before.File(p)) == 0 {
			_, exists, e := ReadFile(root, p)
			if e != nil {
				return nil, e
			}
			if exists {
				return nil, fail(409, "New path collides with an existing file (including case/Unicode aliases)")
			}
		}
		if flag(c["delete"]) {
			if _, ok := bodies[p]; !ok {
				return nil, fail(404, "Cannot delete missing file")
			}
			delete(bodies, p)
		} else {
			if c["text"] != nil {
				bodies[p] = []byte(str(c["text"]))
			}
			if c["base64"] != nil {
				b, e := base64.StdEncoding.Strict().DecodeString(str(c["base64"]))
				if e != nil {
					return nil, fail(422, "Invalid Base64")
				}
				bodies[p] = b
			}
			if _, ok := bodies[p]; !ok {
				return nil, fail(404, "Metadata-only update requires an existing file")
			}
			if c["meta"] != nil {
				metas[p] = c["meta"]
			}
		}
	}
	after, e := s.prepare(spec, before, bodies, metas, str(q["message"]), actor)
	if e != nil {
		return nil, e
	}
	candidate := flag(q["dry_run"]) || flag(q["require_pass"]) && obj(after.Snapshot["verification"])["decision"] != "PASS"
	status := "pending"
	if candidate {
		status = "candidate"
	}
	e = s.transaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec("INSERT INTO versions(id,project,bundle,status) VALUES(?,?,?,?)", after.ID, project, string(JSON(after.Snapshot)), status); e != nil {
			return e
		}
		if _, e := tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?)", project, actor.ID, q["request_id"], requestDigest, after.ID); e != nil {
			return e
		}
		_, e := audit(tx, project, actor, "write.intent", M{"version": after.ID, "request_id": q["request_id"], "request_digest": requestDigest, "model": q["model"], "task": q["task"], "tool": q["tool"]})
		return e
	})
	if e != nil {
		return nil, e
	}
	if !candidate {
		if e = s.apply(project, after.ID, actor); e != nil {
			return nil, e
		}
	}
	return s.writeResult(project, after.ID, actor)
}
func (s *Store) writeResult(project, id string, actor Actor) (M, error) {
	v, e := s.Version(project, id, nil, actor)
	if e != nil {
		return nil, e
	}
	status := v.Status
	if status == "candidate" && obj(v.Snapshot["verification"])["decision"] != "PASS" {
		status = "blocked"
	}
	return M{"version": id, "saved": v.Status == "completed", "published": false, "status": status, "verification": v.Snapshot["verification"], "availability": v.AvailableAt, "message": v.Snapshot["message"]}, nil
}
func moves(before, after *Version) map[string]string {
	result := map[string]string{}
	used := map[string]bool{}
	for _, to := range keys(after.Files()) {
		if len(before.File(to)) > 0 {
			continue
		}
		new := after.File(to)
		candidates := []string{}
		for _, from := range keys(before.Files()) {
			if len(after.File(from)) == 0 && !used[from] && before.File(from)["sha256"] == new["sha256"] {
				candidates = append(candidates, from)
			}
		}
		chosen := ""
		for _, v := range list(new["lineage"]) {
			i := obj(v)
			if i["role"] == "data" && i["sha256"] == new["sha256"] && slices.Contains(candidates, str(i["path"])) {
				chosen = str(i["path"])
			}
		}
		if chosen == "" && len(candidates) == 1 {
			chosen = candidates[0]
		}
		if chosen != "" {
			result[chosen] = to
			used[chosen] = true
		}
	}
	return result
}
func allPaths(a, b *Version) []string { return unique(append(keys(a.Files()), keys(b.Files())...)) }
func (s *Store) apply(project, id string, actor Actor) error {
	if e := s.recovered(project); e != nil {
		return e
	}
	spec, root, _, e := s.Project(project, actor)
	if e != nil {
		return e
	}
	after, e := s.Version(project, id, nil, actor)
	if e != nil {
		return e
	}
	if after.Snapshot["author"] != actor.ID || after.Status != "pending" {
		return fail(409, "This write cannot be resumed")
	}
	before, e := s.Version(project, str(after.Snapshot["base_version"]), nil, actor)
	if e != nil {
		return e
	}
	latest, e := s.Version(project, "latest", nil, actor)
	if e != nil {
		return e
	}
	if latest.ID != before.ID {
		return fail(409, "STALE: write baseline changed")
	}
	if e = matches(root, spec, before); e != nil {
		return e
	}
	if _, e = s.DB.Exec("UPDATE versions SET status='applying' WHERE id=?", id); e != nil {
		return e
	}
	applied := map[string]bool{}
	moved := map[string]bool{}
	apply := func() error {
		for from, to := range moves(before, after) {
			e := Move(root, from, to, str(before.File(from)["sha256"]))
			if e == nil || wasApplied(e) {
				applied[from], applied[to] = true, true
			}
			if e != nil {
				return e
			}
			moved[from], moved[to] = true, true
		}
		for _, p := range allPaths(before, after) {
			if moved[p] || before.File(p)["sha256"] == after.File(p)["sha256"] {
				continue
			}
			f := after.File(p)
			var b []byte
			if len(f) > 0 {
				_, _, b, e = s.Source(str(f["source_id"]), project, actor)
				if e != nil {
					return e
				}
			}
			e := Replace(root, p, b, len(f) > 0, str(before.File(p)["sha256"]))
			if e == nil || wasApplied(e) {
				applied[p] = true
			}
			if e != nil {
				return e
			}
		}
		if e := matches(root, spec, after); e != nil {
			return e
		}
		if _, e := s.Version(project, id, nil, actor); e != nil {
			return e
		}
		return s.transaction(func(tx *sql.Tx) error { return complete(tx, project, id, actor) })
	}
	if e = apply(); e != nil {
		var completed bool
		if err := s.DB.QueryRow("SELECT available_at IS NOT NULL FROM versions WHERE id=?", id).Scan(&completed); err != nil {
			return err
		}
		if completed {
			return e
		}
		recovery := s.restore(root, before, after, applied, actor)
		status := "pending"
		if recovery != nil {
			status = "recovery_required"
		}
		if _, err := s.DB.Exec("UPDATE versions SET status=? WHERE id=?", status, id); err != nil {
			return err
		}
		if recovery != nil {
			return fail(409, "RECOVERY_REQUIRED: external conflict or rollback failure")
		}
		return e
	}
	return nil
}
func (s *Store) restore(root string, before, after *Version, applied map[string]bool, actor Actor) error {
	moved := map[string]bool{}
	for from, to := range moves(before, after) {
		moved[from], moved[to] = true, true
		if applied != nil && !applied[from] && !applied[to] {
			continue
		}
		expected := str(before.File(from)["sha256"])
		a, ae, e := ReadFile(root, from)
		if e != nil {
			return e
		}
		b, be, e := ReadFile(root, to)
		if e != nil {
			return e
		}
		ah, bh := originalHash(a, ae), originalHash(b, be)
		switch {
		case ah == expected && !be:
		case ah == expected && bh == expected:
			if e = Replace(root, to, nil, false, expected); e != nil {
				return e
			}
		case !ae && bh == expected:
			if e = Move(root, to, from, expected); e != nil {
				return e
			}
		default:
			return fail(409, "Recovery conflict; preserve external edits")
		}
	}
	for _, p := range allPaths(before, after) {
		if moved[p] || applied != nil && !applied[p] || before.File(p)["sha256"] == after.File(p)["sha256"] {
			continue
		}
		a, b := before.File(p), after.File(p)
		body, exists, e := ReadFile(root, p)
		if e != nil {
			return e
		}
		actual := originalHash(body, exists)
		if actual == str(a["sha256"]) {
			continue
		}
		if actual != str(b["sha256"]) {
			return fail(409, "Recovery conflict; preserve external edits")
		}
		var original []byte
		if len(a) > 0 {
			_, _, original, e = s.Source(str(a["source_id"]), str(before.Snapshot["project_id"]), actor)
			if e != nil {
				return e
			}
		}
		if e = Replace(root, p, original, len(a) > 0, actual); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) Recover(project, id string, actor Actor) (M, error) {
	if e := actor.Require("editor"); e != nil {
		return nil, e
	}
	_, root, _, e := s.Project(project, actor)
	if e != nil {
		return nil, e
	}
	after, e := s.Version(project, id, nil, actor)
	if e != nil {
		return nil, e
	}
	if after.Snapshot["author"] != actor.ID || after.Status != "applying" && after.Status != "recovery_required" {
		return nil, fail(409, "Recovery requires your own interrupted write")
	}
	before, e := s.Version(project, str(after.Snapshot["base_version"]), nil, actor)
	if e != nil {
		return nil, e
	}
	if e = s.restore(root, before, after, nil, actor); e != nil {
		return nil, e
	}
	e = s.transaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec("UPDATE versions SET status='pending' WHERE id=?", id); e != nil {
			return e
		}
		_, e := audit(tx, project, actor, "write.recovered", M{"version": id})
		return e
	})
	return M{"status": "recovered", "version": id, "retry": "Use the original request ID and identical payload"}, e
}
func publicFile(f M) M {
	v := clone(f)
	if v["metadata"] != nil {
		obj(v["metadata"])["declared_by"] = f["declared_by"]
		obj(v["metadata"])["content_sha256"] = f["metadata_sha256"]
	}
	return v
}
func (s *Store) List(project string, q M, actor Actor) (M, error) {
	v, e := s.Version(project, str(q["version"]), q["as_of"], actor)
	if e != nil {
		return nil, e
	}
	files := []any{}
	for _, p := range keys(v.Files()) {
		files = append(files, publicFile(v.File(p)))
	}
	return M{"project_id": project, "version": v.ID, "files": files, "verification": v.Snapshot["verification"], "temporal": temporal(v, q["as_of"])}, nil
}
func (s *Store) Read(project string, q M, actor Actor) (M, error) {
	v, e := s.Version(project, str(q["version"]), q["as_of"], actor)
	if e != nil {
		return nil, e
	}
	p := str(q["path"])
	if p == "" {
		return nil, fail(422, "Read requires path")
	}
	f := v.File(p)
	if len(f) == 0 {
		return nil, fail(404, "File outside selected version")
	}
	_, _, b, e := s.Source(str(f["source_id"]), project, actor)
	if e != nil {
		return nil, e
	}
	r := publicFile(f)
	r["version"] = v.ID
	x := documents.Extract(p, b)
	var text any
	if len(x.Fragments) > 0 {
		lines := []string{}
		for _, f := range x.Fragments {
			lines = append(lines, f.Text)
		}
		text = strings.Join(lines, "\n")
	}
	r["text"], r["extraction"], r["fragments"] = text, x.Info, x.Fragments
	if len(x.Sheets) > 0 {
		r["sheets"] = x.Sheets
	}
	r["temporal"] = temporal(v, q["as_of"])
	if flag(q["include_bytes"]) {
		r["base64"] = base64.StdEncoding.EncodeToString(b)
	}
	r["receipt"], e = s.Receipt(project, actor, "file.read", M{"version": v.ID, "path": p, "sha256": f["sha256"], "as_of": q["as_of"]})
	return r, e
}
func (s *Store) Versions(project string, actor Actor) (M, error) {
	_, _, active, e := s.Project(project, actor)
	if e != nil {
		return nil, e
	}
	rows, e := s.DB.Query("SELECT id,status,available_at,approver,revoked FROM versions WHERE project=? ORDER BY rowid DESC", project)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []any{}
	for rows.Next() {
		var id, status string
		var available, approver *string
		var revoked bool
		if e = rows.Scan(&id, &status, &available, &approver, &revoked); e != nil {
			return nil, e
		}
		result = append(result, M{"version": id, "status": status, "available_at": available, "approver": approver, "revoked": revoked})
	}
	return M{"project_id": project, "versions": result, "active_release": active}, rows.Err()
}
func (s *Store) reviewGuard(project string, v *Version, actor Actor) error {
	q, _ := Query(M{})
	g, e := s.Guard(project, v, keys(v.Files()), q, actor, false)
	if e != nil {
		return e
	}
	if g["decision"] != "PASS" {
		return fail(409, "Current declarations or input checks require repair/review")
	}
	return nil
}
func (s *Store) Review(project, id string, actor Actor) (M, error) {
	if e := actor.Operator("reviewer"); e != nil {
		return nil, e
	}
	v, e := s.Version(project, id, nil, actor)
	if e != nil {
		return nil, e
	}
	if v.Snapshot["author"] == actor.ID || v.Status != "completed" || obj(v.Snapshot["verification"])["decision"] != "PASS" {
		return nil, fail(409, "Review requires an independent identity and a completed passing snapshot")
	}
	if e = s.reviewGuard(project, v, actor); e != nil {
		return nil, e
	}
	e = s.transaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec("UPDATE versions SET approver=? WHERE id=?", actor.ID, id); e != nil {
			return e
		}
		_, e := audit(tx, project, actor, "version.approved", M{"version": id})
		return e
	})
	return M{"version": id, "approver": actor.ID}, e
}
func (s *Store) Publish(project, id string, expected *string, actor Actor) (M, error) {
	if e := actor.Operator("publisher"); e != nil {
		return nil, e
	}
	spec, root, active, e := s.Project(project, actor)
	if e != nil {
		return nil, e
	}
	v, e := s.Version(project, id, nil, actor)
	if e != nil {
		return nil, e
	}
	if v.Approver == nil || *v.Approver == str(v.Snapshot["author"]) || v.Status != "completed" || obj(v.Snapshot["verification"])["decision"] != "PASS" {
		return nil, fail(409, "Publication requires passing checks and independent approval")
	}
	if !equal(active, expected) {
		return nil, fail(409, "Active release changed")
	}
	if e = matches(root, spec, v); e != nil {
		return nil, e
	}
	if e = s.reviewGuard(project, v, actor); e != nil {
		return nil, e
	}
	e = s.transaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec("UPDATE projects SET active=? WHERE id=?", id, project); e != nil {
			return e
		}
		_, e := audit(tx, project, actor, "version.published", M{"version": id, "expected_active": expected})
		return e
	})
	return M{"project_id": project, "active_release": id}, e
}
func (s *Store) Revoke(project, id string, actor Actor) (M, error) {
	if e := actor.Operator("publisher"); e != nil {
		return nil, e
	}
	if _, _, _, e := s.Project(project, actor); e != nil {
		return nil, e
	}
	e := s.transaction(func(tx *sql.Tx) error {
		r, e := tx.Exec("UPDATE versions SET revoked=1 WHERE id=? AND project=?", id, project)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return fail(404, "Version not found")
		}
		if _, e = tx.Exec("UPDATE projects SET active=NULL WHERE id=? AND active=?", project, id); e != nil {
			return e
		}
		_, e = audit(tx, project, actor, "version.revoked", M{"version": id})
		return e
	})
	return M{"version": id, "revoked": true}, e
}
func (s *Store) Trace(project string, cut any, actor Actor) (M, error) {
	if _, _, _, e := s.Project(project, actor); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query("SELECT project,event,previous,hash FROM audit ORDER BY seq")
	if e != nil {
		return nil, e
	}
	events := []M{}
	previous := ""
	for rows.Next() {
		var scope, body, prev, hash string
		if e = rows.Scan(&scope, &body, &prev, &hash); e != nil {
			rows.Close()
			return nil, e
		}
		value, e := StrictJSON([]byte(body))
		if e != nil {
			rows.Close()
			return nil, e
		}
		event := obj(value)
		if prev != previous || event["project_id"] != scope || digest(M{"previous": prev, "event": event}) != hash {
			rows.Close()
			return nil, fail(409, "Audit chain integrity failed")
		}
		previous = hash
		if scope == project && (cut == nil || str(event["recorded_at"]) <= str(cut)) {
			events = append(events, event)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	visible := []any{}
	readable := map[string]bool{}
	for i := len(events) - 1; i >= 0 && len(visible) < 200; i-- {
		event := events[i]
		data := obj(event["data"])
		ok := false
		if id := str(data["version"]); id != "" {
			var seen bool
			ok, seen = readable[id]
			if !seen {
				_, e := s.Version(project, id, cut, actor)
				ok = e == nil
				readable[id] = ok
			}
		} else if id := str(data["source_id"]); id != "" {
			_, _, _, e := s.Source(id, project, actor)
			ok = e == nil
		}
		if ok {
			visible = append(visible, event)
		}
	}
	return M{"project_id": project, "events": visible, "chain_verified": true}, nil
}
func (s *Store) AuthAgent(project, path string, readOnly bool, actor Actor) (M, error) {
	if e := actor.Operator("editor"); e != nil {
		return nil, e
	}
	if _, _, _, e := s.Project(project, actor); e != nil {
		return nil, e
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(abs))
	if e != nil {
		return nil, e
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	f, e := privateFile(abs, false)
	if e != nil {
		return nil, e
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxFile+1))
	f.Close()
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
	roles := []string{"reader"}
	if !readOnly {
		roles = append(roles, "editor")
	}
	a := Actor{ID: "agent-" + randomID()[:24], Roles: roles, Audience: "agent", WorkspaceProjects: []string{project}}
	token := randomID()
	t[token] = a
	if e = Replace(filepath.Dir(abs), filepath.Base(abs), JSON(t), true, Hash(b)); e != nil {
		return nil, e
	}
	return M{"FILEWISE_TOKEN": token, "actor": a, "restart_required": true}, nil
}
