//go:build windows

package mygoui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Each native case needs its own main-thread event loop. Ordinary unit tests
// still use MyGo's headless tester and never open windows.
func TestMain(m *testing.M) {
	if mode := os.Getenv("GOLDER_WINDOWS_COMPAT_CHILD"); mode != "" {
		runWindowsCompatibilityChild(mode)
		return
	}
	os.Exit(m.Run())
}

func TestWindowsNativeCompatibility(t *testing.T) {
	if os.Getenv("GOLDER_WINDOWS_GUI_E2E") != "1" {
		t.Skip("set GOLDER_WINDOWS_GUI_E2E=1 in a Windows desktop session")
	}
	for _, mode := range []string{"menu-alt", "quit-during-picker"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), "GOLDER_WINDOWS_COMPAT_CHILD="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "compatibility check passed") {
				t.Fatalf("%s: %v: %s", mode, err, out)
			}
		})
	}
}

func runWindowsCompatibilityChild(mode string) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1) // child only; never a force-close of the user's GUI
	}
	mygo.App.SetName("Golder Windows compatibility")
	var passed atomic.Bool
	var win *mygo.Window
	items := desktopMenuItems("windows", func() { win.Close() })
	mygo.App.SetMenu(mygo.NewMenu(items))
	mygo.App.WhenReady(func() {
		win = mygo.NewWindow(mygo.WindowOptions{
			Title: "Golder Windows compatibility", Width: 500, Height: 350,
			Content: ui.View(func(c *ui.Context) { ui.Text(c, "Native menu and dialog compatibility") }),
		})
		if mode == "quit-during-picker" {
			a := &App{win: win}
			deferred := false
			win.OnClose(func(event *mygo.CloseEvent) {
				if a.deferCloseForFileDialog() {
					deferred = true
					event.PreventDefault()
				} else if deferred {
					passed.Store(true)
				}
			})
			_, complete, ok := a.beginFileDialog()
			if !ok {
				fail(fmt.Errorf("picker refused its live owner"))
			}
			go func() {
				_, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "Golder compatibility picker"})
				if err != nil {
					fail(err)
				}
				complete(func() { fail(fmt.Errorf("shutdown applied a stale picker result")) })
			}()
			go func() {
				if err := waitWindowsCondition("owned file picker open", func() bool {
					parent := win.NativeHandle()
					popup, _, _ := dialogUser32.NewProc("GetLastActivePopup").Call(parent)
					owner, _, _ := dialogGetOwner.Call(popup, 4)
					var class [32]uint16
					dialogGetClassName.Call(popup, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
					return popup != parent && owner == parent && syscall.UTF16ToString(class[:]) == "#32770"
				}); err != nil {
					fail(err)
				}
				// Mirrors QuitDuringDialog, but exercises Golder's guarded close
				// with a real parented file picker instead of an app-modal message.
				win.Close()
			}()
			return
		}
		if mode != "menu-alt" {
			fail(fmt.Errorf("unknown compatibility mode %q", mode))
		}
		go func() {
			parent := win.NativeHandle()
			foreground, _, _ := dialogUser32.NewProc("SetForegroundWindow").Call(parent)
			if foreground == 0 {
				fail(fmt.Errorf("could not give keyboard focus to the test window"))
			}
			// As in MyGo's TestMenuBarClosesOnAlt: enter the bar, Down opens
			// the first menu, and Alt must close it. Do not silently skip failures.
			dialogPostMessage.Call(parent, 0x0112, 0xF100, 0) // WM_SYSCOMMAND / SC_KEYMENU
			if err := waitWindowsCondition("menu bar active", func() bool { flags, ok := windowsMenuFlags(parent); return ok && flags&0x04 != 0 }); err != nil {
				fail(err)
			}
			pressWindowsCompatibilityKey(0x28) // VK_DOWN
			if err := waitWindowsCondition("first menu open", func() bool { return windowsPopupCount(parent) > 0 }); err != nil {
				fail(err)
			}
			pressWindowsCompatibilityKey(0x12) // VK_MENU
			if err := waitWindowsCondition("Alt closed menu", func() bool { return windowsPopupCount(parent) == 0 }); err != nil {
				fail(err)
			}
			passed.Store(true)
			mygo.RunOnMain(func() { items[0].Submenu[0].Click(nil, win) })
		}()
	})
	if err := mygo.App.Run(); err != nil {
		fail(err)
	}
	if !passed.Load() {
		fail(fmt.Errorf("application quit before the compatibility assertion completed"))
	}
	fmt.Println("compatibility check passed")
}

func pressWindowsCompatibilityKey(key uintptr) {
	proc := dialogUser32.NewProc("keybd_event")
	proc.Call(key, 0, 0, 0)
	proc.Call(key, 0, 2, 0)
}

func windowsMenuFlags(parent uintptr) (uint32, bool) {
	var info struct {
		Size, Flags                                   uint32
		Active, Focus, Capture, Menu, MoveSize, Caret uintptr
		Rect                                          [4]int32
	}
	info.Size = uint32(unsafe.Sizeof(info))
	thread, _, _ := dialogThreadID.Call(parent, 0)
	ok, _, _ := dialogUser32.NewProc("GetGUIThreadInfo").Call(thread, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0, false
	}
	return info.Flags, true
}

// Menu-bar dropdowns are not necessarily GUI_POPUPMENUMODE, which describes
// a popup menu loop rather than every visible dropdown. Observe Windows' menu
// windows on this app's thread, as MyGo's upstream test does.
func windowsPopupCount(parent uintptr) int {
	thread, _, _ := dialogThreadID.Call(parent, 0)
	if thread == 0 {
		return -1
	}
	class, _ := syscall.UTF16PtrFromString("#32768")
	find := dialogUser32.NewProc("FindWindowExW")
	visible := dialogUser32.NewProc("IsWindowVisible")
	count := 0
	for after := uintptr(0); ; {
		hwnd, _, _ := find.Call(0, after, uintptr(unsafe.Pointer(class)), 0)
		if hwnd == 0 {
			return count
		}
		after = hwnd
		ownerThread, _, _ := dialogThreadID.Call(hwnd, 0)
		shown, _, _ := visible.Call(hwnd)
		if ownerThread == thread && shown != 0 {
			count++
		}
	}
}

func waitWindowsCondition(name string, check func() bool) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if check() {
			return nil
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("%s did not happen within 5 seconds", name)
		case <-tick.C:
		}
	}
}
