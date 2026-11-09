//go:build windows

package mygoui

import (
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	dialogUser32       = syscall.NewLazyDLL("user32.dll")
	dialogThreadID     = dialogUser32.NewProc("GetWindowThreadProcessId")
	dialogEnumWindows  = dialogUser32.NewProc("EnumThreadWindows")
	dialogGetOwner     = dialogUser32.NewProc("GetWindow")
	dialogGetClassName = dialogUser32.NewProc("GetClassNameW")
	dialogPostMessage  = dialogUser32.NewProc("PostMessageW")
	dialogClosePosted  atomic.Bool // enumeration and callbacks run on the UI thread
	// NewCallback allocations cannot be freed; create one, not one per picker.
	dialogCloseCallback = syscall.NewCallback(func(hwnd, parent uintptr) uintptr {
		owner, _, _ := dialogGetOwner.Call(hwnd, 4) // GW_OWNER
		if owner != parent {
			return 1
		}
		var name [32]uint16
		n, _, _ := dialogGetClassName.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if n == 0 || syscall.UTF16ToString(name[:]) != "#32770" {
			return 1
		}
		posted, _, _ := dialogPostMessage.Call(hwnd, 0x0010, 0, 0) // WM_CLOSE: normal Cancel path
		if posted != 0 {
			dialogClosePosted.Store(true)
			return 0
		}
		return 1
	})
)

func dismissOwnedFileDialog(parent uintptr) bool {
	if parent == 0 {
		return false
	}
	thread, _, _ := dialogThreadID.Call(parent, 0)
	if thread == 0 {
		return false
	}
	// The callback stops enumeration only after posting a normal close request
	// to a common dialog with this exact owner. No application window is killed.
	dialogClosePosted.Store(false)
	dialogEnumWindows.Call(thread, dialogCloseCallback, parent)
	return dialogClosePosted.Load()
}
