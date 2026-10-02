//go:build darwin || linux

package workspace

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func stateFile(db, suffix string) string {
	return strings.TrimSuffix(db, filepath.Ext(db)) + "." + suffix
}
func StatePath(path string) (string, error) {
	if path == ".filewise-go/filewise.db" {
		path = DefaultStatePath()
	}
	s, e := Open(path)
	if e != nil {
		return "", e
	}
	defer s.Close()
	return s.Path, nil
}
func privateRead(path string) ([]byte, error) {
	f, e := privateFile(path, false)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if len(b) > MaxFile {
		return nil, fail(413, "Private state exceeds limit")
	}
	return b, e
}
func privateWrite(path string, b []byte) error {
	if f, e := privateFile(path, false); e == nil {
		f.Close()
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	old, exists, e := ReadFile(filepath.Dir(path), filepath.Base(path))
	if e != nil {
		return e
	}
	return Replace(filepath.Dir(path), filepath.Base(path), b, true, originalHash(old, exists))
}
func serviceLock(db string) (*os.File, error) {
	f, e := privateFile(stateFile(db, "service.lock"), true)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		if errors.Is(e, unix.EWOULDBLOCK) {
			return nil, fail(409, "A Filewise service already owns this database")
		}
		return nil, e
	}
	return f, nil
}
func uiTokens(db string, create bool) (Tokens, error) {
	path := stateFile(db, "ui-tokens.json")
	if _, e := os.Lstat(path); errors.Is(e, os.ErrNotExist) && create {
		t := Tokens{randomID(): Actor{ID: "filewise-local-ui", Roles: []string{"reader", "editor"}, Audience: "operator", WorkspaceProjects: []string{}}}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(JSON(t))
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return nil, e
		}
	}
	return ReadTokens(path)
}
func uiToken(tokens Tokens) (string, error) {
	for token, a := range tokens {
		if a.ID == "filewise-local-ui" && a.Audience == "operator" && len(a.Roles) == 2 && contains(a.Roles, "reader") && contains(a.Roles, "editor") {
			return token, nil
		}
	}
	return "", fail(403, "Local UI operator credential is missing")
}
func runtimeState(db string) (M, error) {
	b, e := privateRead(stateFile(db, "runtime.json"))
	if e != nil {
		return nil, e
	}
	v, e := StrictJSON(b)
	if e != nil {
		return nil, e
	}
	m := obj(v)
	u, e := url.Parse(str(m["url"]))
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !hashRE.MatchString(str(m["instance"])) {
		return nil, fail(409, "Runtime URL must be a local origin")
	}
	return m, nil
}
func serviceRequest(db string, state M, op string) (M, error) {
	t, e := uiTokens(db, false)
	if e != nil {
		return nil, e
	}
	token, e := uiToken(t)
	if e != nil {
		return nil, e
	}
	method := "GET"
	if op == "stop" {
		method = "POST"
	}
	body := M{}
	if op == "stop" {
		body["instance"] = state["instance"]
	}
	req, e := http.NewRequest(method, str(state["url"])+"/api/local/"+op, bytes.NewReader(JSON(body)))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		return nil, fail(503, "Filewise is not running; run filewise start")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fail(503, "Service identity check failed")
	}
	b, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil {
		return nil, e
	}
	if len(b) > 65536 {
		return nil, fail(413, "Service response exceeds limit")
	}
	v, e := StrictJSON(b)
	return obj(v), e
}
func ServiceStatus(db string) (M, error) {
	state, e := runtimeState(db)
	if e != nil {
		return nil, e
	}
	lock, e := serviceLock(db)
	if e == nil {
		lock.Close()
		return nil, fail(503, "Filewise is not running; run filewise start")
	}
	if e.Error() != "A Filewise service already owns this database" {
		return nil, e
	}
	live, e := serviceRequest(db, state, "status")
	if e != nil {
		return nil, e
	}
	if live["instance"] != state["instance"] || !equal(live["pid"], state["pid"]) {
		return nil, fail(409, "Service identity changed; refusing to control another process")
	}
	return live, nil
}
func StopService(db string) (M, error) {
	live, e := ServiceStatus(db)
	if e != nil {
		return nil, e
	}
	state, e := runtimeState(db)
	if e != nil {
		return nil, e
	}
	if state["instance"] != live["instance"] {
		return nil, fail(409, "Service identity changed; refusing to control another process")
	}
	if _, e = serviceRequest(db, state, "stop"); e != nil {
		return nil, e
	}
	for i := 0; i < 100; i++ {
		lock, e := serviceLock(db)
		if e == nil {
			lock.Close()
			auto, _ := Autostart(db, nil, 8733)
			return M{"stopped": true, "login_startup": auto["enabled"]}, nil
		}
		if e.Error() != "A Filewise service already owns this database" {
			return nil, e
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fail(409, "Shutdown is still waiting for current work; retry status")
}
func StartService(db string, port int, openBrowser bool) (M, error) {
	tokens, e := uiTokens(db, true)
	if e != nil {
		return nil, e
	}
	token, e := uiToken(tokens)
	if e != nil {
		return nil, e
	}
	if _, e = ServiceStatus(db); e != nil {
		guard, e := serviceLock(db)
		if e != nil {
			return nil, e
		}
		logPath := stateFile(db, "service.log")
		log, e := privateFile(logPath, true)
		if e != nil {
			guard.Close()
			return nil, e
		}
		if _, e = log.Seek(0, io.SeekEnd); e != nil {
			guard.Close()
			log.Close()
			return nil, e
		}
		exe, e := os.Executable()
		if e != nil {
			guard.Close()
			log.Close()
			return nil, e
		}
		guard.Close()
		cmd := exec.Command(exe, "--db", db, "serve", "--local-ui", "--tokens", stateFile(db, "ui-tokens.json"), "--port", strconv.Itoa(port))
		cmd.Stdin = nil
		cmd.Stdout = log
		cmd.Stderr = log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if e = cmd.Start(); e != nil {
			log.Close()
			return nil, e
		}
		log.Close()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		ready := false
		for i := 0; i < 100; i++ {
			if _, e = ServiceStatus(db); e == nil {
				ready = true
				break
			}
			select {
			case <-exited:
				return nil, fail(503, "Background service exited; inspect "+logPath)
			default:
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ready {
			_ = cmd.Process.Kill()
			<-exited
			return nil, fail(503, "Background service did not become ready")
		}
	}
	live, e := ServiceStatus(db)
	if e != nil {
		return nil, e
	}
	u := str(live["url"]) + "/#token=" + token
	if openBrowser {
		command := "xdg-open"
		if runtime.GOOS == "darwin" {
			command = "/usr/bin/open"
		}
		cmd := exec.Command(command, u)
		if cmd.Start() == nil {
			go cmd.Wait()
		}
	}
	return M{"running": true, "url": u, "db": db, "log": stateFile(db, "service.log"), "note": "Browser may be closed; the background process keeps monitoring. The URL contains an operator editing credential; do not share."}, nil
}
func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func Autostart(db string, enable *bool, port int) (M, error) {
	if runtime.GOOS != "darwin" {
		if enable != nil && *enable {
			return nil, fail(501, "Login startup is currently implemented for macOS only")
		}
		return M{"supported": false, "enabled": false}, nil
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return nil, e
	}
	path := filepath.Join(home, "Library", "LaunchAgents", "local.filewise.go."+Hash([]byte(db))[:16]+".plist")
	if enable != nil {
		if *enable {
			if _, e = uiTokens(db, true); e != nil {
				return nil, e
			}
			path, e = privatePath(path)
			if e != nil {
				return nil, e
			}
			exe, e := os.Executable()
			if e != nil {
				return nil, e
			}
			args := []string{exe, "--db", db, "serve", "--local-ui", "--tokens", stateFile(db, "ui-tokens.json"), "--port", strconv.Itoa(port)}
			items := ""
			for _, arg := range args {
				items += "<string>" + xmlText(arg) + "</string>"
			}
			label := xmlText(strings.TrimSuffix(filepath.Base(path), ".plist"))
			log := xmlText(stateFile(db, "service.log"))
			plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + label + `</string><key>ProgramArguments</key><array>` + items + `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><false/><key>StandardOutPath</key><string>` + log + `</string><key>StandardErrorPath</key><string>` + log + `</string></dict></plist>`
			if e = privateWrite(path, []byte(plist)); e != nil {
				return nil, e
			}
		} else if f, e := privateFile(path, false); e == nil {
			f.Close()
			if e = os.Remove(path); e != nil {
				return nil, e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, fail(403, "Refusing to remove an unmanaged login item")
		}
	}
	_, e = os.Lstat(path)
	return M{"supported": true, "enabled": e == nil, "path": path, "takes_effect": "next_login", "note": "No system-wide service; keep the executable and state at their current paths. Not a crash supervisor."}, nil
}
func PickFolder(locale string) (M, error) {
	prompt := ""
	switch locale {
	case "en":
		prompt = `POSIX path of (choose folder with prompt "Choose a folder for Filewise to monitor")`
	case "zh-CN":
		prompt = `POSIX path of (choose folder with prompt "选择要由 Filewise 持续监控的文件夹")`
	default:
		return nil, fail(422, "Unsupported UI locale")
	}
	if runtime.GOOS != "darwin" {
		return nil, fail(501, "Paste an absolute folder path on this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	b, e := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", prompt).Output()
	if e != nil {
		return nil, fail(409, "Folder selection cancelled or unavailable; paste the path instead")
	}
	return M{"root": strings.TrimSpace(string(b))}, nil
}
