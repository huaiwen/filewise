//go:build darwin || linux

package workspace

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

const Help = `Filewise — Go-native versioned files and evidence

  filewise start [--no-open] [--port 8733]
  filewise status|stop
  filewise autostart [--enable|--disable]
  filewise document inspect|compare|capture|record ...
  filewise auth-init --out PRIVATE_TOKENS
  filewise --db PRIVATE_DB project add ROOT --id ID --name NAME [--spec FILE]
  filewise --db PRIVATE_DB project sync|status PROJECT
  filewise --db PRIVATE_DB auth-agent PROJECT --tokens PRIVATE_TOKENS [--read-only]
  filewise --db PRIVATE_DB serve --tokens PRIVATE_TOKENS [--host 127.0.0.1] [--port 8000]
  filewise --db PRIVATE_DB --actor ID --roles ROLE project review|publish|revoke PROJECT VERSION
  filewise agent [--url ORIGIN] projects|versions|ls|read|write|delete|sync|search|diff|impact|resolve|quality|compile|verify|recover|trace ...

Agent commands use FILEWISE_URL and FILEWISE_TOKEN, never local workspace storage.
Working saves do not approve or publish. Preview and save use different request IDs;
retries keep the same ID and payload. Original user metadata and derived semantic
change metadata are stored separately. Existing databases are not auto-converted.
`

type options struct {
	values map[string][]string
	args   []string
}

func parseOptions(args []string, values, switches, multiple string) (options, error) {
	o := options{values: map[string][]string{}}
	names := strings.Fields(values)
	bools := strings.Fields(switches)
	repeat := strings.Fields(multiple)
	literal := false
	for i := 0; i < len(args); i++ {
		s := args[i]
		if s == "--" && !literal {
			literal = true
			continue
		}
		if literal || !strings.HasPrefix(s, "-") || s == "-" {
			o.args = append(o.args, s)
			continue
		}
		if s == "-m" {
			s = "--message"
		}
		if s == "-h" {
			s = "--help"
		}
		key, value, has := strings.Cut(strings.TrimPrefix(s, "--"), "=")
		if !strings.HasPrefix(s, "--") || !slices.Contains(names, key) && !slices.Contains(bools, key) {
			return o, fail(422, "Unknown option: "+s)
		}
		if _, ok := o.values[key]; ok && !slices.Contains(repeat, key) {
			return o, fail(422, "Repeated option: --"+key)
		}
		if slices.Contains(bools, key) {
			if has {
				return o, fail(422, "Switch takes no value: --"+key)
			}
			value = "true"
		} else if !has {
			i++
			if i >= len(args) {
				return o, fail(422, "Missing value for --"+key)
			}
			value = args[i]
		}
		o.values[key] = append(o.values[key], value)
	}
	return o, nil
}
func (o options) get(key, defaultValue string) string {
	v := o.values[key]
	if len(v) == 0 {
		return defaultValue
	}
	return v[0]
}
func (o options) has(key string) bool { _, ok := o.values[key]; return ok }
func inputBytes(path string, in io.Reader, limit int64) ([]byte, error) {
	var r io.Reader = in
	if path != "-" {
		f, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() {
			return nil, fail(422, "Input must be a regular file or stdin")
		}
		r = f
	}
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, fail(413, "Input file exceeds limit")
	}
	return b, nil
}
func loadInput(path string, in io.Reader) (any, error) {
	b, e := inputBytes(path, in, 70<<20)
	if e != nil {
		return nil, e
	}
	return StrictJSON(b)
}
func requireArgs(o options, min, max int) error {
	if len(o.args) < min || len(o.args) > max {
		return fail(422, "Wrong number of command arguments; use --help")
	}
	return nil
}
func printJSON(out io.Writer, v any) error {
	_, e := fmt.Fprintln(out, string(JSON(v)))
	if e != nil {
		return e
	}
	m := obj(v)
	if m["decision"] == "BLOCKED" || m["decision"] == "NEEDS_REVIEW" || m["status"] == "blocked" {
		return fail(2, "Decision requires repair or review")
	}
	return nil
}

func CLI(ctx context.Context, args []string, in io.Reader, out, diagnostic io.Writer) error {
	global := []string{}
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		key, _, has := strings.Cut(strings.TrimPrefix(args[0], "--"), "=")
		if !slices.Contains([]string{"db", "actor", "roles"}, key) {
			break
		}
		global = append(global, args[0])
		args = args[1:]
		if !has {
			if len(args) == 0 {
				return fail(422, "Missing global option value")
			}
			global = append(global, args[0])
			args = args[1:]
		}
	}
	g, e := parseOptions(global, "db actor roles", "", "")
	if e != nil {
		return e
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, e := io.WriteString(out, Help)
		return e
	}
	command := args[0]
	args = args[1:]
	if command == "agent" {
		v, e := agentCLI(args, in, out)
		if e != nil {
			return e
		}
		if v == nil {
			return nil
		}
		return printJSON(out, v)
	}
	db := g.get("db", os.Getenv("FILEWISE_DB"))
	if db == "" {
		db = ".filewise-go/filewise.db"
	}
	actor := Actor{ID: g.get("actor", "local-editor"), Roles: strings.Split(g.get("roles", "editor"), ","), Audience: "operator", WorkspaceProjects: []string{}}
	if e = actor.Validate(); e != nil {
		return e
	}
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		_, e := io.WriteString(out, Help)
		return e
	}
	switch command {
	case "start", "status", "stop", "autostart":
		values, switches := "", ""
		if command == "start" {
			values, switches = "port", "no-open"
		}
		if command == "autostart" {
			values, switches = "port", "enable disable"
		}
		o, e := parseOptions(args, values, switches, "")
		if e != nil {
			return e
		}
		if e = requireArgs(o, 0, 0); e != nil {
			return e
		}
		port, e := strconv.Atoi(o.get("port", "8733"))
		if e != nil || port < 0 || port > 65535 {
			return fail(422, "Invalid port")
		}
		if o.has("enable") && o.has("disable") {
			return fail(422, "Choose --enable or --disable")
		}
		path, e := StatePath(db)
		if e != nil {
			return e
		}
		var v M
		switch command {
		case "start":
			v, e = StartService(path, port, !o.has("no-open"))
		case "status":
			v, e = ServiceStatus(path)
		case "stop":
			v, e = StopService(path)
		case "autostart":
			var enabled *bool
			if o.has("enable") || o.has("disable") {
				value := o.has("enable")
				enabled = &value
			}
			v, e = Autostart(path, enabled, port)
		}
		if e != nil {
			return e
		}
		return printJSON(out, v)
	case "auth-init":
		o, e := parseOptions(args, "out", "", "")
		if e != nil {
			return e
		}
		if e = requireArgs(o, 0, 0); e != nil {
			return e
		}
		v, e := AuthInit(o.get("out", ".filewise-go/tokens.json"))
		if e != nil {
			return e
		}
		return printJSON(out, v)
	case "serve":
		o, e := parseOptions(args, "tokens host port", "local-ui", "")
		if e != nil {
			return e
		}
		if e = requireArgs(o, 0, 0); e != nil {
			return e
		}
		tokens, e := ReadTokens(o.get("tokens", ".filewise-go/tokens.json"))
		if e != nil {
			return e
		}
		port, e := strconv.Atoi(o.get("port", "8000"))
		if e != nil || port < 0 || port > 65535 {
			return fail(422, "Invalid port")
		}
		if e = Serve(ctx, db, tokens, o.get("host", "127.0.0.1"), port, o.has("local-ui"), diagnostic); e != nil {
			return e
		}
		return printJSON(out, M{"stopped": true})
	case "auth-agent":
		o, e := parseOptions(args, "tokens", "read-only", "")
		if e != nil {
			return e
		}
		if e = requireArgs(o, 1, 1); e != nil {
			return e
		}
		s, e := Open(db)
		if e != nil {
			return e
		}
		defer s.Close()
		v, e := s.AuthAgent(o.args[0], o.get("tokens", ".filewise-go/tokens.json"), o.has("read-only"), actor)
		if e != nil {
			return e
		}
		return printJSON(out, v)
	case "project":
		if len(args) == 0 {
			return fail(422, "Project command required")
		}
		op := args[0]
		o, e := parseOptions(args[1:], "id name spec include expected-active", "", "include")
		if e != nil {
			return e
		}
		count := 1
		if slices.Contains([]string{"review", "publish", "revoke", "recover"}, op) {
			count = 2
		}
		if e = requireArgs(o, count, count); e != nil {
			return e
		}
		if !slices.Contains([]string{"add", "sync", "status", "review", "publish", "revoke", "recover"}, op) {
			return fail(422, "Unknown project command")
		}
		s, e := Open(db)
		if e != nil {
			return e
		}
		defer s.Close()
		var v M
		switch op {
		case "add":
			raw := M{}
			if o.has("spec") {
				value, e := loadInput(o.get("spec", ""), in)
				if e != nil {
					return e
				}
				var ok bool
				raw, ok = value.(map[string]any)
				if !ok {
					return fail(422, "Project spec must be an object")
				}
			}
			raw["id"], raw["name"] = o.get("id", ""), o.get("name", "")
			if o.has("include") {
				raw["includes"] = o.values["include"]
			}
			v, e = s.Create(raw, o.args[0], actor)
		case "sync":
			v, e = s.Sync(o.args[0], actor)
		case "status":
			v, e = s.Versions(o.args[0], actor)
		case "review":
			v, e = s.Review(o.args[0], o.args[1], actor)
		case "publish":
			var expected *string
			if o.has("expected-active") {
				value := o.get("expected-active", "")
				if err := Exact(value); err != nil {
					return err
				}
				expected = &value
			}
			v, e = s.Publish(o.args[0], o.args[1], expected, actor)
		case "revoke":
			v, e = s.Revoke(o.args[0], o.args[1], actor)
		case "recover":
			v, e = s.Recover(o.args[0], o.args[1], actor)
		}
		if e != nil {
			return e
		}
		return printJSON(out, v)
	default:
		return fail(422, "Unknown or pending Go command; use --help")
	}
}
func agentCLI(args []string, in io.Reader, out io.Writer) (any, error) {
	origin := os.Getenv("FILEWISE_URL")
	if origin == "" {
		origin = "http://127.0.0.1:8000"
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "--url") {
		oargs := args[:1]
		if args[0] == "--url" {
			if len(args) < 2 {
				return nil, fail(422, "Missing --url")
			}
			oargs = args[:2]
		}
		o, e := parseOptions(oargs, "url", "", "")
		if e != nil {
			return nil, e
		}
		origin = o.get("url", origin)
		args = args[len(oargs):]
	}
	if len(args) == 0 || slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		_, e := io.WriteString(out, Help+"\nWrite: PROJECT PATH --base VERSION --request-id ID --content TEXT|--file FILE|--meta JSON -m MESSAGE [--dry-run] [--require-pass]\nCompile: PROJECT --goal TEXT [--path PATH] [--checks FILE] [--query TEXT]\nVerify: PROJECT [--task-id ID] [--phase build|preflight|postflight] [--result FILE]\n")
		return nil, e
	}
	op := args[0]
	args = args[1:]
	common := "url version as-of path"
	values := common
	switches := ""
	multi := "path"
	min, max := 1, 1
	switch op {
	case "projects":
		values = "url"
		min, max = 0, 0
	case "versions", "sync":
		values = "url"
	case "ls", "resolve", "trace":
	case "read":
		values += " output"
		min, max = 2, 2
	case "write":
		values = "url base request-id message content file meta request task model"
		switches = "dry-run require-pass"
		min, max = 1, 2
	case "delete":
		values = "url base request-id message"
		min, max = 2, 2
	case "search":
		values += " mode limit tag max-per-file requirements"
		multi += " tag"
		min, max = 2, 2
	case "diff":
		values = "url path as-of"
		min, max = 2, 3
	case "impact":
		values += " base-version direction"
	case "quality":
		values += " requirements"
	case "compile":
		values += " goal query base-version direction retrieval-mode retrieval-limit max-chars checks requirements model-use"
	case "verify":
		values += " phase task-id operation result outputs citations output-version"
	case "recover":
		values = "url version"
	default:
		return nil, fail(422, "Unknown Agent command")
	}
	o, e := parseOptions(args, values, switches, multi)
	if e != nil {
		return nil, e
	}
	if e = requireArgs(o, min, max); e != nil {
		return nil, e
	}
	origin = o.get("url", origin)
	if op == "projects" {
		return Request(origin, "projects", "GET", nil)
	}
	project := o.args[0]
	if e = ID(project); e != nil {
		return nil, e
	}
	prefix := "workspaces/" + project + "/"
	if op == "versions" {
		return Request(origin, prefix+op, "GET", nil)
	}
	if op == "sync" {
		return Request(origin, prefix+op, "POST", M{})
	}
	if op == "write" || op == "delete" {
		q := M{}
		if o.has("request") {
			if len(o.args) != 1 || len(o.values) > 1+boolInt(o.has("url")) {
				return nil, fail(422, "Bulk --request cannot be mixed with single-file flags")
			}
			v, e := loadInput(o.get("request", ""), in)
			if e != nil {
				return nil, e
			}
			var ok bool
			q, ok = v.(map[string]any)
			if !ok {
				return nil, fail(422, "Write request must be an object")
			}
		} else {
			if len(o.args) != 2 {
				return nil, fail(422, "Write requires path")
			}
			change := M{}
			if op == "delete" {
				change["delete"] = true
			} else {
				if o.has("content") && o.has("file") {
					return nil, fail(422, "Choose --content or --file")
				}
				if o.has("content") {
					change["text"] = o.get("content", "")
				}
				if o.has("file") {
					b, e := inputBytes(o.get("file", ""), in, MaxFile)
					if e != nil {
						return nil, e
					}
					change["base64"] = base64.StdEncoding.EncodeToString(b)
				}
				if o.has("meta") {
					v, e := StrictJSON([]byte(o.get("meta", "")))
					if e != nil {
						return nil, e
					}
					change["meta"] = v
				}
			}
			q = M{"base_version": o.get("base", ""), "request_id": o.get("request-id", randomID()), "changes": M{o.args[1]: change}, "message": o.get("message", ""), "dry_run": o.has("dry-run"), "require_pass": o.has("require-pass"), "task": o.get("task", ""), "model": o.get("model", ""), "tool": "filewise.agent." + op + ".go"}
		}
		q, e = WriteQuery(q)
		if e != nil {
			return nil, e
		}
		return Request(origin, prefix+"write", "POST", q)
	}
	q := M{}
	for k, v := range o.values {
		field := strings.ReplaceAll(k, "-", "_")
		switch k {
		case "url", "output":
			continue
		case "path":
			q["paths"] = v
		case "tag":
			q["tags"] = v
		case "checks", "requirements", "result", "outputs", "citations":
			value, e := loadInput(v[0], in)
			if e != nil {
				return nil, e
			}
			if k == "checks" {
				field = "output_checks"
			}
			q[field] = value
		case "limit", "max-per-file", "retrieval-limit", "max-chars":
			n, e := strconv.Atoi(v[0])
			if e != nil {
				return nil, fail(422, "Invalid integer option")
			}
			q[field] = n
		default:
			q[field] = v[0]
		}
	}
	switch op {
	case "read":
		q["path"], q["include_bytes"] = o.args[1], o.has("output")
	case "search":
		q["query"] = o.args[1]
	case "diff":
		q["before"] = o.args[1]
		if len(o.args) == 3 {
			q["after"] = o.args[2]
		}
	case "recover":
		if !o.has("version") {
			return nil, fail(422, "Recovery requires --version")
		}
	}
	q, e = Query(q)
	if e != nil {
		return nil, e
	}
	v, e := Request(origin, prefix+op, "POST", q)
	if e != nil {
		return nil, e
	}
	if op == "read" && o.has("output") {
		return Export(o.get("output", ""), obj(v))
	}
	return v, nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// DefaultStatePath is separate from historical runtimes and the document journal.
func DefaultStatePath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Filewise-Go", "filewise.db")
	}
	return filepath.Join(home, ".local", "share", "filewise-go", "filewise.db")
}
