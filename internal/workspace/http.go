package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

//go:embed ui/index.html ui/app.js ui/i18n.js ui/strings.json ui/style.css
var ui embed.FS

type Local struct {
	URL, Instance string
	Stop          context.CancelFunc
}

// Gateway retains a fixed credential map. Credential changes require restart.
type Gateway struct {
	Store  *Store
	Tokens Tokens
	Local  *Local
}

func Handler(store *Store, tokens Tokens) (http.Handler, error) {
	if e := ValidateTokens(tokens); e != nil {
		return nil, e
	}
	return &Gateway{Store: store, Tokens: tokens}, nil
}
func (g *Gateway) locked(fn func(*Store) (any, error)) (any, error) {
	if e := g.Store.Lock(); e != nil {
		return nil, e
	}
	defer g.Store.Unlock()
	return fn(g.Store)
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	respond := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if g.Local != nil {
		if r.Host != strings.TrimPrefix(g.Local.URL, "http://") || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != g.Local.URL {
			respond(403, M{"error": "Local UI requires its exact loopback Host and Origin"})
			return
		}
	}
	assets := map[string]string{"/": "ui/index.html", "/ui/app.js": "ui/app.js", "/ui/i18n.js": "ui/i18n.js", "/ui/strings.json": "ui/strings.json", "/ui/style.css": "ui/style.css"}
	if g.Local != nil && (r.Method == "GET" || r.Method == "HEAD") {
		if file, ok := assets[r.URL.Path]; ok {
			mime := map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".json": "application/json; charset=utf-8", ".css": "text/css; charset=utf-8"}
			w.Header().Set("Content-Type", mime[filepath.Ext(file)])
			b, e := ui.ReadFile(file)
			if e != nil {
				respond(500, M{"error": "Embedded asset missing"})
				return
			}
			if r.Method == "GET" {
				_, _ = w.Write(b)
			}
			return
		}
	}
	result, e := g.handle(w, r)
	if e != nil {
		status := Status(e)
		message := e.Error()
		if status == 500 {
			message = "Internal storage error"
		}
		respond(status, M{"error": message, "code": ErrorCode(message)})
		return
	}
	respond(200, result)
}
func (g *Gateway) handle(w http.ResponseWriter, r *http.Request) (any, error) {
	if r.URL.Path == "/health" && r.Method == "GET" {
		return M{"status": "ok", "runtime": "go"}, nil
	}
	if r.URL.Path == "/" && r.Method == "GET" {
		return M{"product": "Filewise", "runtime": "Go", "guide": "docs/go-migration.md", "browser_workbench": "start_with_local_ui"}, nil
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return nil, fail(404, "Endpoint not available")
	}
	provided := r.Header.Get("Authorization")
	var actor Actor
	found := false
	// Hash first so every comparison is fixed length, including malformed input.
	if strings.HasPrefix(provided, "Bearer ") {
		wanted := sha256.Sum256([]byte(provided[7:]))
		for token, a := range g.Tokens {
			known := sha256.Sum256([]byte(token))
			if subtle.ConstantTimeCompare(wanted[:], known[:]) == 1 {
				actor = a
				found = true
			}
		}
	}
	if !found {
		return nil, fail(401, "Bearer credential required")
	}
	if r.URL.RawQuery != "" {
		return nil, fail(422, "Query strings are not accepted")
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if len(parts) == 1 && r.Method == "GET" {
		if parts[0] == "me" {
			return actor, nil
		}
		if parts[0] == "projects" {
			return g.locked(func(s *Store) (any, error) { return s.Projects(actor) })
		}
	}
	if len(parts) == 2 && parts[0] == "local" {
		return g.localAPI(w, r, parts[1], actor)
	}
	if len(parts) == 3 && parts[0] == "workspaces" {
		project, op := parts[1], parts[2]
		if e := actor.Access(project); e != nil {
			return nil, e
		}
		if op == "versions" && r.Method == "GET" {
			return g.locked(func(s *Store) (any, error) { return s.Versions(project, actor) })
		}
		if r.Method != "POST" {
			return nil, fail(405, "Use POST JSON")
		}
		if op == "write" || op == "sync" || op == "recover" {
			if e := actor.Require("editor"); e != nil {
				return nil, e
			}
		}
		limit := int64(11 << 20)
		if op == "write" {
			limit = 70 << 20
		}
		body, e := requestBody(w, r, limit)
		if e != nil {
			return nil, e
		}
		return g.locked(func(s *Store) (any, error) {
			switch op {
			case "write":
				return s.Write(project, body, actor)
			case "sync":
				if len(body) != 0 {
					return nil, fail(422, "sync accepts an empty object")
				}
				return s.Sync(project, actor)
			default:
				return s.Dispatch(project, op, body, actor)
			}
		})
	}
	if len(parts) == 5 && parts[0] == "projects" {
		project, id, op := parts[1], parts[3], parts[4]
		if parts[2] == "versions" && r.Method == "POST" {
			role := "publisher"
			if op == "approve" {
				role = "reviewer"
			}
			if e := actor.Operator(role); e != nil {
				return nil, e
			}
			body, e := requestBody(w, r, 8192)
			if e != nil {
				return nil, e
			}
			body, e = normalize(body, M{"expected_active": nil})
			if e != nil {
				return nil, e
			}
			var expected *string
			if body["expected_active"] != nil {
				v := str(body["expected_active"])
				if e := Exact(v); e != nil {
					return nil, e
				}
				expected = &v
			}
			return g.locked(func(s *Store) (any, error) {
				switch op {
				case "approve":
					return s.Review(project, id, actor)
				case "publish":
					return s.Publish(project, id, expected, actor)
				case "revoke":
					return s.Revoke(project, id, actor)
				default:
					return nil, fail(404, "Unknown release operation")
				}
			})
		}
		if parts[2] == "sources" && op == "policy" && r.Method == "PUT" {
			if e := actor.Operator("reviewer"); e != nil {
				return nil, e
			}
			body, e := requestBody(w, r, 8192)
			if e != nil {
				return nil, e
			}
			if len(body) != 2 {
				return nil, fail(422, "Policy requires acl and revoked")
			}
			body, e = normalize(body, M{"acl": []any{}, "revoked": false})
			if e != nil {
				return nil, e
			}
			return g.locked(func(s *Store) (any, error) {
				return s.Policy(project, id, strs(body["acl"]), flag(body["revoked"]), actor)
			})
		}
	}
	return nil, fail(404, "Unknown API operation")
}
func (g *Gateway) localAPI(w http.ResponseWriter, r *http.Request, op string, actor Actor) (any, error) {
	if g.Local == nil {
		return nil, fail(404, "Local UI is disabled on this gateway")
	}
	if e := actor.Operator("editor"); e != nil {
		return nil, e
	}
	read := op == "overview" || op == "status" || op == "autostart"
	if r.Method != "POST" && !(read && r.Method == "GET") {
		return nil, fail(405, "Use POST JSON")
	}
	body, e := requestBody(w, r, 65536)
	if e != nil {
		return nil, e
	}
	if op == "register" {
		return g.locked(func(s *Store) (any, error) { return s.Register(body, actor) })
	}
	q, e := normalize(body, M{"project": nil, "rules": nil, "revision": nil, "job": nil, "action": nil, "enabled": nil, "locale": nil, "instance": nil})
	if e != nil {
		return nil, e
	}
	switch op {
	case "pick":
		locale := "en"
		if q["locale"] != nil {
			locale = str(q["locale"])
		}
		return PickFolder(locale)
	case "status":
		return M{"running": true, "instance": g.Local.Instance, "url": g.Local.URL, "pid": os.Getpid()}, nil
	case "stop":
		if q["instance"] != nil && q["instance"] != g.Local.Instance {
			return nil, fail(409, "Service identity changed; refusing to control another process")
		}
		g.Local.Stop()
		return M{"stopping": true}, nil
	}
	return g.locked(func(s *Store) (any, error) {
		switch op {
		case "overview":
			return s.Overview(actor)
		case "configure":
			if str(q["project"]) == "" || q["rules"] == nil {
				return nil, fail(422, "Project and rules required")
			}
			return s.Configure(str(q["project"]), obj(q["rules"]), q["revision"], actor)
		case "action":
			return s.WatchAction(str(q["job"]), str(q["action"]), actor)
		case "autostart":
			var enabled *bool
			if q["enabled"] != nil {
				value, ok := q["enabled"].(bool)
				if !ok {
					return nil, fail(422, "Enabled must be boolean")
				}
				enabled = &value
			}
			u, _ := url.Parse(g.Local.URL)
			port, _ := strconv.Atoi(u.Port())
			return Autostart(s.Path, enabled, port)
		default:
			return nil, fail(404, "Unknown local operation")
		}
	})
}
func requestBody(w http.ResponseWriter, r *http.Request, limit int64) (M, error) {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if e != nil {
		return nil, fail(413, "Request body limit exceeded")
	}
	if len(b) == 0 {
		return M{}, nil
	}
	v, e := StrictJSON(b)
	if e != nil {
		return nil, e
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fail(422, "Request must be a JSON object")
	}
	return m, nil
}
func Serve(ctx context.Context, db string, tokens Tokens, host string, port int, localUI bool, diagnostic io.Writer) error {
	if e := ValidateTokens(tokens); e != nil {
		return e
	}
	if localUI && host != "127.0.0.1" {
		return fail(422, "Local UI must bind to 127.0.0.1")
	}
	s, e := Open(db)
	if e != nil {
		return e
	}
	defer s.Close()
	owner, e := serviceLock(s.Path)
	if e != nil {
		return e
	}
	defer owner.Close()
	if e = s.ResumeWatch(); e != nil {
		return e
	}
	if e = unix.Flock(int(s.lock.Fd()), unix.LOCK_UN); e != nil {
		return e
	}
	listener, e := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if e != nil {
		return e
	}
	defer listener.Close()
	signalContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	work, cancel := context.WithCancel(signalContext)
	defer cancel()
	g := &Gateway{Store: s, Tokens: tokens}
	if localUI {
		g.Local = &Local{URL: "http://" + listener.Addr().String(), Instance: randomID(), Stop: cancel}
		if e = privateWrite(stateFile(s.Path, "runtime.json"), JSON(M{"url": g.Local.URL, "instance": g.Local.Instance, "pid": os.Getpid()})); e != nil {
			return e
		}
	}
	server := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 300 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			if work.Err() != nil {
				return
			}
			if e := s.WatchTick(int(time.Now().Unix())); e != nil {
				_, _ = io.WriteString(diagnostic, "Watcher tick failed: "+e.Error()+"\n")
			}
			select {
			case <-work.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	shutdownDone := make(chan struct{})
	go func() { defer close(shutdownDone); <-work.Done(); _ = server.Shutdown(context.Background()) }()
	_, e = io.WriteString(diagnostic, "Filewise Go listening on http://"+listener.Addr().String()+"\n")
	if e == nil {
		e = server.Serve(listener)
	}
	cancel()
	<-shutdownDone
	<-watchDone
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func Request(origin, path, method string, body any) (any, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Host == "" {
		return nil, fail(422, "Use an HTTP(S) origin without credentials, path or query")
	}
	if u.Scheme == "http" && !slicesHost(u.Hostname()) {
		return nil, fail(422, "Remote gateways require HTTPS")
	}
	token := os.Getenv("FILEWISE_TOKEN")
	if len(token) < 32 || len(token) > 256 {
		return nil, fail(401, "Set FILEWISE_TOKEN to a project credential")
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return nil, fail(401, "Invalid credential")
		}
	}
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(JSON(body))
	}
	req, e := http.NewRequest(method, strings.TrimSuffix(origin, "/")+"/api/"+path, input)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 300 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		return nil, fail(503, "Gateway unavailable: "+e.Error())
	}
	defer response.Body.Close()
	b, e := io.ReadAll(io.LimitReader(response.Body, (100<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 100<<20 {
		return nil, fail(413, "Response exceeds client limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := "Gateway " + response.Status
		v, err := StrictJSON(b)
		if err == nil {
			if s := str(obj(v)["error"]); s != "" {
				message += ": " + s
			}
		}
		if len(message) > 2000 {
			message = message[:2000]
		}
		return nil, fail(response.StatusCode, message)
	}
	return StrictJSON(b)
}
func slicesHost(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }
func Export(path string, result M) (M, error) {
	b, e := base64.StdEncoding.Strict().DecodeString(str(result["base64"]))
	if e != nil || Hash(b) != str(result["sha256"]) {
		return nil, fail(409, "Gateway content digest mismatch")
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return nil, e
	}
	if e = f.Sync(); e != nil {
		return nil, e
	}
	return M{"output": path, "receipt": result["receipt"]}, nil
}
