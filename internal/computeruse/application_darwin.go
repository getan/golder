//go:build darwin && cgo

package computeruse

/*
#include <stdlib.h>
int cu_application_metadata(const char *path, char *identity, size_t identityLength, char *display, size_t displayLength, char *name, size_t nameLength);
int cu_launch_background(const char *path, const char *const *args, int count);
int cu_frontmost_pid(void);
int cu_activate_pid(int32_t pid);
int cu_frontmost_bundle_is(const char *bundleID);
uint64_t cu_active_space(void);
int cu_restore_space(uint64_t space);
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"
)

type macApplication struct {
	application Application
	path        string
	bundleID    string
	file        os.FileInfo
}

func macApplicationMetadata(path string) (bundleID, display, name string, ok bool) {
	input := C.CString(path)
	defer C.free(unsafe.Pointer(input))
	var bundle, label, bundleName [1024]C.char
	if C.cu_application_metadata(input, &bundle[0], C.size_t(len(bundle)), &label[0], C.size_t(len(label)), &bundleName[0], C.size_t(len(bundleName))) == 0 {
		return "", "", "", false
	}
	return C.GoString(&bundle[0]), C.GoString(&label[0]), C.GoString(&bundleName[0]), true
}

func macApplicationRoots() []string {
	roots := []string{"/System/Applications", "/Applications"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, filepath.Join(home, "Applications"))
	}
	return roots
}

// Applications inspects standard application directories without accepting a
// model-provided path. It keeps duplicate names distinct for human selection.
func (c *MacWindowCatalog) Applications(ctx context.Context, requested string) ([]Application, error) {
	return c.applicationsIn(ctx, requested, macApplicationRoots())
}

func (c *MacWindowCatalog) applicationsIn(ctx context.Context, requested string, roots []string) ([]Application, error) {
	want := normalizeApplicationName(requested)
	if want == "" {
		return nil, nil
	}
	var found []macApplication
	seen := make(map[string]bool)
	visited := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				if path == root && errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			visited++
			if visited > 20000 {
				return errors.New("application inventory exceeded its safe limit")
			}
			if !entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if path != root && (strings.HasPrefix(entry.Name(), ".") || strings.Count(rel, string(filepath.Separator)) >= 3) {
				return filepath.SkipDir
			}
			if !strings.EqualFold(filepath.Ext(entry.Name()), ".app") {
				return nil
			}
			canonical, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			if seen[canonical] {
				return filepath.SkipDir
			}
			seen[canonical] = true
			bundleID, display, bundleName, ok := macApplicationMetadata(canonical)
			if !ok {
				return filepath.SkipDir
			}
			base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			matches := false
			for _, candidate := range expandApplicationAliases([]string{base, display, bundleName}) {
				if normalizeApplicationName(candidate) == want {
					matches = true
					break
				}
			}
			// Calculator's display name is not localized in every macOS bundle.
			if strings.EqualFold(bundleID, "com.apple.calculator") && want == "计算器" {
				matches = true
			}
			if !matches {
				return filepath.SkipDir
			}
			file, err := os.Stat(canonical)
			if err != nil {
				return err
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return err
			}
			if display == "" {
				display = base
			}
			app := Application{ID: hex.EncodeToString(nonce[:]), Name: display, Identity: canonical, Detail: path, BundleID: bundleID}
			found = append(found, macApplication{application: app, path: canonical, bundleID: bundleID, file: file})
			return filepath.SkipDir
		})
		if err != nil {
			return nil, fmt.Errorf("application inventory: %w", err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.applications = make(map[string]macApplication, len(found))
	out := make([]Application, 0, len(found))
	for _, app := range found {
		c.applications[app.application.ID] = app
		out = append(out, app.application)
	}
	return out, nil
}

// macApplicationLaunchArgs opens the exact catalogued app without replacing
// the Golder GUI or terminal as the foreground application.
func macApplicationLaunchArgs(path string) []string {
	args := []string{"-g", "-a", path}
	if flags := macApplicationBackgroundArguments(path); len(flags) > 0 {
		args = append(args, "--args")
		args = append(args, flags...)
	}
	return args
}

func macApplicationBackgroundArguments(path string) []string {
	if !macApplicationIsChromium(path) {
		return nil
	}
	// Without these, Chromium stops painting when its window is occluded or
	// on another Space, so a background preview stays on the last frame.
	return []string{
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-background-timer-throttling",
	}
}

func macApplicationIsChromium(path string) bool {
	for _, framework := range []string{"Electron Framework.framework", "Chromium Embedded Framework.framework"} {
		if info, err := os.Stat(filepath.Join(path, "Contents", "Frameworks", framework)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func macApplicationRevealArgs(path string) []string {
	return []string{"-a", path}
}

// LaunchApplication launches only a candidate issued by this catalog after
// rechecking its directory and bundle identity. No shell interprets its name.
func (c *MacWindowCatalog) LaunchApplication(ctx context.Context, app Application) error {
	return c.openApplication(ctx, app, false)
}

// RevealApplication brings forward only the same locally catalogued app after
// a background launch left no visible window, without choosing a window.
func (c *MacWindowCatalog) RevealApplication(ctx context.Context, app Application) error {
	return c.openApplication(ctx, app, true)
}

func (c *MacWindowCatalog) openApplication(ctx context.Context, app Application, reveal bool) error {
	c.mu.Lock()
	candidate, ok := c.applications[app.ID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok || candidate.application != app {
		return errors.New("application candidate changed or expired")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := os.Stat(candidate.path)
	if err != nil || !os.SameFile(candidate.file, current) {
		return errors.New("application bundle changed before launch")
	}
	bundleID, _, _, ok := macApplicationMetadata(candidate.path)
	if !ok || bundleID != candidate.bundleID {
		return errors.New("application bundle identity changed before launch")
	}
	if reveal {
		if err := exec.CommandContext(ctx, "open", macApplicationRevealArgs(candidate.path)...).Run(); err != nil {
			return fmt.Errorf("launch %s: %w", app.Name, err)
		}
		return nil
	}
	previous := C.cu_frontmost_pid()
	space := C.cu_active_space()
	application := C.CString(candidate.path)
	defer C.free(unsafe.Pointer(application))
	flags := macApplicationBackgroundArguments(candidate.path)
	cargs := make([]*C.char, len(flags))
	for i, flag := range flags {
		cargs[i] = C.CString(flag)
		defer C.free(unsafe.Pointer(cargs[i]))
	}
	var argv **C.char
	if len(cargs) > 0 {
		argv = (**C.char)(unsafe.Pointer(&cargs[0]))
	}
	if C.cu_launch_background(application, argv, C.int(len(cargs))) == 0 {
		if err := exec.CommandContext(ctx, "open", macApplicationLaunchArgs(candidate.path)...).Run(); err != nil {
			return fmt.Errorf("launch %s: %w", app.Name, err)
		}
		if space != 0 && C.cu_active_space() != space {
			C.cu_restore_space(space)
		}
		return nil
	}
	// Some Electron apps activate themselves during startup and drag the
	// active Space with them. Space changes are asynchronous, so re-assert the
	// previous Space and frontmost app until the launch settles.
	if previous > 0 || space != 0 {
		bundleID := candidate.bundleID
		go func(previous C.int32_t, space C.uint64_t, bundleID string) {
			bundle := C.CString(bundleID)
			defer C.free(unsafe.Pointer(bundle))
			start := time.Now()
			stable := 0
			deadline := start.Add(6 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(200 * time.Millisecond)
				if previous > 0 && C.cu_frontmost_bundle_is(bundle) != 0 {
					C.cu_activate_pid(previous)
				}
				if space != 0 && C.cu_active_space() != space {
					C.cu_restore_space(space)
				}
				if (space == 0 || C.cu_active_space() == space) && (previous == 0 || C.cu_frontmost_bundle_is(bundle) == 0) {
					stable++
					if stable >= 3 && time.Since(start) >= 2*time.Second {
						break
					}
				} else {
					stable = 0
				}
			}
		}(previous, space, bundleID)
	}
	return nil
}
