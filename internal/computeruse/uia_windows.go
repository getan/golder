//go:build windows

package computeruse

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed uia_windows.ps1 wgc_windows.cs
var windowsUIAScript embed.FS

var windowsUser32 = windows.NewLazySystemDLL("user32.dll")
var windowsGetTopWindow = windowsUser32.NewProc("GetTopWindow")
var windowsGetWindow = windowsUser32.NewProc("GetWindow")
var windowsGetWindowText = windowsUser32.NewProc("GetWindowTextW")
var windowsGetClassName = windowsUser32.NewProc("GetClassNameW")
var windowsIsIconic = windowsUser32.NewProc("IsIconic")

type windowsTarget struct {
	hwnd        uintptr
	pid         uint32
	start       uint64
	executable  string
	class       string
	application string
	title       string
}

type windowsBackend struct {
	mu         sync.Mutex
	targets    map[string]windowsTarget
	refs       map[string]string
	snapshotID string
	targetID   string
	rootID     string
	closed     bool
	script     string
	wgc        string
	powershell string
}

func newWindowsBackend() (*windowsBackend, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, ErrUnavailable
	}
	powershell := filepath.Join(dir, "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powershell); err != nil {
		return nil, fmt.Errorf("%w: Windows UI Automation host unavailable", ErrUnavailable)
	}
	script, err := windowsUIAScript.ReadFile("uia_windows.ps1")
	if err != nil {
		return nil, err
	}
	wgc, err := windowsUIAScript.ReadFile("wgc_windows.cs")
	if err != nil {
		return nil, err
	}
	return &windowsBackend{targets: make(map[string]windowsTarget), powershell: powershell, script: base64.StdEncoding.EncodeToString(script), wgc: base64.StdEncoding.EncodeToString(wgc)}, nil
}

func windowsText(hwnd uintptr, proc *windows.LazyProc) string {
	buf := make([]uint16, 512)
	n, _, _ := proc.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || n >= uintptr(len(buf)-1) {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func protectedWindowsExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	for _, protected := range []string{"golder.exe", "tuios.exe", "windowsterminal.exe", "conhost.exe", "cmd.exe", "powershell.exe", "pwsh.exe", "explorer.exe", "systemsettings.exe", "credentialuibroker.exe", "consent.exe", "logonui.exe"} {
		if name == protected {
			return true
		}
	}
	return false
}

func windowsElevation(token windows.Token) (uint32, error) {
	var value, length uint32
	err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &length)
	if err != nil || length != uint32(unsafe.Sizeof(value)) {
		return 0, ErrTargetLost
	}
	return value, nil
}

func windowsProcessIdentity(pid uint32) (uint64, string, error) {
	if pid == 0 || pid == uint32(os.Getpid()) {
		return 0, "", ErrTargetLost
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, "", ErrTargetLost
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return 0, "", ErrTargetLost
	}
	defer token.Close()
	owner, err := token.GetTokenUser()
	if err != nil {
		return 0, "", ErrTargetLost
	}
	self := windows.GetCurrentProcessToken()
	current, err := self.GetTokenUser()
	if err != nil || !owner.User.Sid.Equals(current.User.Sid) {
		return 0, "", ErrTargetLost
	}
	level, err := windowsElevation(token)
	selfLevel, selfErr := windowsElevation(self)
	if err != nil || selfErr != nil || level != selfLevel {
		return 0, "", ErrTargetLost
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, "", ErrTargetLost
	}
	buf := make([]uint16, 32768)
	length := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(process, 0, &buf[0], &length); err != nil || length == 0 {
		return 0, "", ErrTargetLost
	}
	path := windows.UTF16ToString(buf[:length])
	if !filepath.IsAbs(path) || protectedWindowsExecutable(path) {
		return 0, "", ErrTargetLost
	}
	start := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return start, path, nil
}

func inspectWindowsTarget(hwnd uintptr) (windowsTarget, error) {
	if hwnd == 0 || !windows.IsWindow(windows.HWND(hwnd)) || !windows.IsWindowVisible(windows.HWND(hwnd)) {
		return windowsTarget{}, ErrTargetLost
	}
	iconic, _, _ := windowsIsIconic.Call(hwnd)
	if iconic != 0 {
		return windowsTarget{}, ErrTargetLost
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.HWND(hwnd), &pid); err != nil || pid == 0 {
		return windowsTarget{}, ErrTargetLost
	}
	start, exe, err := windowsProcessIdentity(pid)
	if err != nil {
		return windowsTarget{}, err
	}
	title := windowsText(hwnd, windowsGetWindowText)
	class := windowsText(hwnd, windowsGetClassName)
	if title == "" || class == "" {
		return windowsTarget{}, ErrTargetLost
	}
	return windowsTarget{hwnd: hwnd, pid: pid, start: start, executable: exe, class: class, application: strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe)), title: title}, nil
}

func (b *windowsBackend) Targets(ctx context.Context) ([]Target, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrUnavailable
	}
	var targets []Target
	next := make(map[string]windowsTarget)
	hwnd, _, _ := windowsGetTopWindow.Call(0)
	seen := make(map[uintptr]bool)
	for hwnd != 0 && len(seen) < 1024 && len(targets) < 128 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[hwnd] {
			break
		}
		seen[hwnd] = true
		if window, err := inspectWindowsTarget(hwnd); err == nil {
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return nil, err
			}
			id := hex.EncodeToString(nonce[:])
			next[id] = window
			targets = append(targets, Target{ID: id, Application: window.application, Window: window.title, Detail: fmt.Sprintf("PID %d · Window %x", window.pid, hwnd)})
		}
		hwnd, _, _ = windowsGetWindow.Call(hwnd, 2) // GW_HWNDNEXT
	}
	b.targets, b.refs, b.snapshotID, b.targetID, b.rootID = next, nil, "", "", ""
	return targets, nil
}

func (b *windowsBackend) validate(ctx context.Context, target Target) (windowsTarget, error) {
	if err := ctx.Err(); err != nil {
		return windowsTarget{}, err
	}
	w, ok := b.targets[target.ID]
	if b.closed || !ok || target.Application != w.application || target.Window != w.title {
		return windowsTarget{}, ErrTargetLost
	}
	now, err := inspectWindowsTarget(w.hwnd)
	if err != nil || now != w {
		return windowsTarget{}, ErrTargetLost
	}
	return w, nil
}

func (b *windowsBackend) Validate(ctx context.Context, target Target, _ bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.validate(ctx, target)
	return err
}

func (b *windowsBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.targets, b.refs = nil, nil
	b.snapshotID, b.targetID, b.rootID = "", "", ""
	return nil
}

type windowsUIARequest struct {
	Op       string   `json:"op"`
	HWND     uint64   `json:"hwnd"`
	PID      uint32   `json:"pid"`
	Start    uint64   `json:"start"`
	Root     string   `json:"root,omitempty"`
	Ref      string   `json:"ref,omitempty"`
	Text     string   `json:"text,omitempty"`
	Geometry string   `json:"geometry,omitempty"`
	Rect     [4]int32 `json:"rect"`
	X        float64  `json:"x,omitempty"`
	Y        float64  `json:"y,omitempty"`
	DX       int      `json:"dx,omitempty"`
	DY       int      `json:"dy,omitempty"`
	Key      string   `json:"key,omitempty"`
}

type windowsUIANode struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Secure   bool   `json:"secure"`
	Editable bool   `json:"editable"`
	Enabled  bool   `json:"enabled"`
}

type windowsUIAResult struct {
	Nodes     []windowsUIANode `json:"nodes"`
	Truncated bool             `json:"truncated"`
	OK        bool             `json:"ok"`
	PNG       []byte           `json:"png"`
	Root      string           `json:"root"`
	Geometry  string           `json:"geometry"`
	Rect      [4]int32         `json:"rect"`
}

type boundedWindowsOutput struct {
	buffer bytes.Buffer
}

func (b *boundedWindowsOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 12<<20 {
		return 0, errors.New("Windows accessibility response too large")
	}
	return b.buffer.Write(p)
}

type boundedWindowsDiagnostic struct{ buffer bytes.Buffer }

func (b *boundedWindowsDiagnostic) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - b.buffer.Len(); remaining > 0 {
		_, _ = b.buffer.Write(p[:min(remaining, len(p))])
	}
	return n, nil
}

const windowsUIABootstrap = `[Console]::InputEncoding=[System.Text.UTF8Encoding]::new($false);$payload=[Console]::In.ReadToEnd()|ConvertFrom-Json;$global:GolderRequest=$payload.request;$global:GolderWGC=$payload.wgc;$source=[System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($payload.script));& ([scriptblock]::Create($source))`

func (b *windowsBackend) run(ctx context.Context, request windowsUIARequest) (windowsUIAResult, error) {
	data, err := json.Marshal(struct {
		Script  string            `json:"script"`
		WGC     string            `json:"wgc"`
		Request windowsUIARequest `json:"request"`
	}{Script: b.script, WGC: b.wgc, Request: request})
	if err != nil {
		return windowsUIAResult{}, err
	}
	cmd := exec.CommandContext(ctx, b.powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsUIABootstrap)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Stdin = bytes.NewReader(data)
	var output boundedWindowsOutput
	var diagnostic boundedWindowsDiagnostic
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return windowsUIAResult{}, ctx.Err()
		}
		if diagnostic.buffer.String() == "GOLDER_CAPTURE_UNAVAILABLE" {
			return windowsUIAResult{}, errors.New("Windows window capture is unavailable or blocked; Windows Graphics Capture and PrintWindow could not capture this window")
		}
		if diagnostic.buffer.String() == "GOLDER_INPUT_PARTIAL" {
			return windowsUIAResult{}, errors.New("Windows input may have been partially dispatched; observe the target before trying again")
		}
		return windowsUIAResult{}, errors.New("Windows UI Automation request failed")
	}
	var result windowsUIAResult
	if err := json.Unmarshal(output.buffer.Bytes(), &result); err != nil {
		return windowsUIAResult{}, errors.New("invalid Windows UI Automation response")
	}
	return result, nil
}

func (b *windowsBackend) Snapshot(ctx context.Context, target Target) (string, []Element, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w, err := b.validate(ctx, target)
	if err != nil {
		return "", nil, false, err
	}
	b.refs, b.snapshotID, b.targetID, b.rootID = nil, "", "", ""
	result, err := b.run(ctx, windowsUIARequest{Op: "snapshot", HWND: uint64(w.hwnd), PID: w.pid, Start: w.start})
	if err != nil {
		return "", nil, false, err
	}
	if _, err := b.validate(ctx, target); err != nil {
		return "", nil, false, err
	}
	if len(result.Nodes) == 0 || len(result.Nodes) > 128 || result.Root == "" || result.Nodes[0].ID != result.Root {
		return "", nil, false, ErrTargetLost
	}
	refs := make(map[string]string, len(result.Nodes))
	seen := make(map[string]bool, len(result.Nodes))
	elements := make([]Element, 0, len(result.Nodes))
	for i, node := range result.Nodes {
		if node.ID == "" || seen[node.ID] {
			return "", nil, false, ErrTargetLost
		}
		seen[node.ID] = true
		ref := fmt.Sprintf("@e%d", i+1)
		refs[ref] = node.ID
		if node.Secure {
			node.Name, node.Editable = "", false
		}
		elements = append(elements, Element{Ref: ref, Role: node.Role, Name: node.Name, Secure: node.Secure, Editable: node.Editable, Enabled: node.Enabled})
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", nil, false, err
	}
	b.refs, b.snapshotID, b.targetID, b.rootID = refs, hex.EncodeToString(nonce[:]), target.ID, result.Root
	return b.snapshotID, elements, result.Truncated, nil
}

func (*windowsBackend) SupportsAction(action string) bool {
	return action == "click" || action == "fill"
}

func (b *windowsBackend) Act(ctx context.Context, target Target, snapshotID, ref, action, text string) error {
	if (action != "click" && action != "fill") || (action == "click" && text != "") || len(text) > 4096 || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return ErrNotAllowed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	w, err := b.validate(ctx, target)
	if err != nil {
		return err
	}
	id, ok := b.refs[ref]
	if !ok || b.snapshotID != snapshotID || b.targetID != target.ID {
		return ErrStaleObservation
	}
	root := b.rootID
	b.refs, b.snapshotID, b.targetID, b.rootID = nil, "", "", ""
	result, err := b.run(ctx, windowsUIARequest{Op: action, HWND: uint64(w.hwnd), PID: w.pid, Start: w.start, Root: root, Ref: id, Text: text})
	if err != nil {
		return err
	}
	if !result.OK {
		return ErrTargetLost
	}
	return nil
}

func (b *windowsBackend) Capture(ctx context.Context, target Target) ([]byte, error) {
	data, _, err := b.CaptureBound(ctx, target)
	return data, err
}

type windowsFrameBinding struct {
	Target   string
	Geometry string
	Rect     [4]int32
	Digest   [32]byte
}

// CaptureBound never falls back to capturing a desktop or screen rectangle.
func (b *windowsBackend) CaptureBound(ctx context.Context, target Target) ([]byte, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w, err := b.validate(ctx, target)
	if err != nil {
		return nil, "", err
	}
	result, err := b.run(ctx, windowsUIARequest{Op: "capture", HWND: uint64(w.hwnd), PID: w.pid, Start: w.start})
	if err != nil {
		return nil, "", err
	}
	if _, err := b.validate(ctx, target); err != nil {
		return nil, "", err
	}
	if len(result.PNG) == 0 || len(result.PNG) > MaxFrameBytes || (result.Geometry != "window" && result.Geometry != "extended") || result.Rect[2] <= result.Rect[0] || result.Rect[3] <= result.Rect[1] {
		return nil, "", errors.New("Windows window capture unavailable or exceeds the size limit")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(result.PNG))
	if err != nil || format != "png" || int64(config.Width) != int64(result.Rect[2])-int64(result.Rect[0]) || int64(config.Height) != int64(result.Rect[3])-int64(result.Rect[1]) || int64(config.Width)*int64(config.Height) > MaxFramePixels {
		return nil, "", errors.New("Windows window capture geometry is invalid")
	}
	binding, err := json.Marshal(windowsFrameBinding{Target: target.ID, Geometry: result.Geometry, Rect: result.Rect, Digest: sha256.Sum256(result.PNG)})
	if err != nil {
		return nil, "", err
	}
	return result.PNG, string(binding), nil
}

func (b *windowsBackend) dispatchFrame(ctx context.Context, target Target, token string, request windowsUIARequest) error {
	var previous windowsFrameBinding
	if json.Unmarshal([]byte(token), &previous) != nil || previous.Target != target.ID {
		return ErrStaleObservation
	}
	_, fresh, err := b.CaptureBound(ctx, target)
	if err != nil {
		return err
	}
	var current windowsFrameBinding
	if json.Unmarshal([]byte(fresh), &current) != nil || previous != current {
		return ErrStaleObservation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	w, err := b.validate(ctx, target)
	if err != nil {
		return err
	}
	request.HWND, request.PID, request.Start, request.Rect, request.Geometry = uint64(w.hwnd), w.pid, w.start, previous.Rect, previous.Geometry
	result, err := b.run(ctx, request)
	if err != nil {
		return err
	}
	if !result.OK {
		return ErrTargetLost
	}
	return nil
}

func (b *windowsBackend) ClickFrame(ctx context.Context, target Target, token string, x, y float64) error {
	if math.IsNaN(x) || math.IsNaN(y) || x < 0 || x >= 1 || y < 0 || y >= 1 {
		return ErrNotAllowed
	}
	return b.dispatchFrame(ctx, target, token, windowsUIARequest{Op: "point-click", X: x, Y: y})
}

func (b *windowsBackend) ScrollFrame(ctx context.Context, target Target, token string, x, y float64, dx, dy int) error {
	if math.IsNaN(x) || math.IsNaN(y) || x < 0 || x >= 1 || y < 0 || y >= 1 || dx < -5 || dx > 5 || dy < -5 || dy > 5 || (dx == 0 && dy == 0) {
		return ErrNotAllowed
	}
	return b.dispatchFrame(ctx, target, token, windowsUIARequest{Op: "point-scroll", X: x, Y: y, DX: dx, DY: dy})
}

func (b *windowsBackend) PressFrame(ctx context.Context, target Target, token, key string) error {
	switch key {
	case "enter", "tab", "escape", "backspace", "delete", "up", "down", "left", "right", "page_up", "page_down", "home", "end", "space":
	default:
		return ErrNotAllowed
	}
	return b.dispatchFrame(ctx, target, token, windowsUIARequest{Op: "key", Key: key})
}
