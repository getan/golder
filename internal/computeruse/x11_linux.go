//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// XCB uses checked requests and returns protocol errors rather than installing
// an Xlib error handler in the GUI process. It is never used in a Wayland
// session: Xwayland cannot report native Wayland occluders or keyboard focus.
var xcbOnce sync.Once
var xcbLoadErr error
var xcbConnect func(*byte, *int32) uintptr
var xcbDisconnect func(uintptr)
var xcbConnectionError func(uintptr) int32
var xcbFileDescriptor func(uintptr) int32
var xcbGetSetup func(uintptr) uintptr
var xcbRootsIterator func(uintptr) xcbScreenIterator
var xcbScreenNext func(*xcbScreenIterator)
var xcbInternAtom func(uintptr, uint8, uint16, *byte) uint32
var xcbInternAtomReply func(uintptr, uint32, *uintptr) uintptr
var xcbGetProperty func(uintptr, uint8, uint32, uint32, uint32, uint32, uint32) uint32
var xcbGetPropertyReply func(uintptr, uint32, *uintptr) uintptr
var xcbPropertyValue func(uintptr) uintptr
var xcbPropertyValueLength func(uintptr) int32
var xcbQueryTree func(uintptr, uint32) uint32
var xcbQueryTreeReply func(uintptr, uint32, *uintptr) uintptr
var xcbTreeChildren func(uintptr) uintptr
var xcbTreeChildrenLength func(uintptr) int32
var xcbGetGeometry func(uintptr, uint32) uint32
var xcbGetGeometryReply func(uintptr, uint32, *uintptr) uintptr
var xcbTranslateCoordinates func(uintptr, uint32, uint32, int16, int16) uint32
var xcbTranslateReply func(uintptr, uint32, *uintptr) uintptr
var xcbGetAttributes func(uintptr, uint32) uint32
var xcbGetAttributesReply func(uintptr, uint32, *uintptr) uintptr
var xcbGetInputFocus func(uintptr) uint32
var xcbGetInputFocusReply func(uintptr, uint32, *uintptr) uintptr
var xcbGrabPointer func(uintptr, uint8, uint32, uint16, uint8, uint8, uint32, uint32, uint32) uint32
var xcbGrabPointerReply func(uintptr, uint32, *uintptr) uintptr
var xcbUngrabPointer func(uintptr, uint32) uint32
var xcbGrabKeyboard func(uintptr, uint8, uint32, uint32, uint8, uint8) uint32
var xcbGrabKeyboardReply func(uintptr, uint32, *uintptr) uintptr
var xcbUngrabKeyboard func(uintptr, uint32) uint32
var xcbQueryPointer func(uintptr, uint32) uint32
var xcbQueryPointerReply func(uintptr, uint32, *uintptr) uintptr
var xcbQueryKeymap func(uintptr) uint32
var xcbQueryKeymapReply func(uintptr, uint32, *uintptr) uintptr
var xcbKeyboardMapping func(uintptr, uint8, uint8) uint32
var xcbKeyboardMappingReply func(uintptr, uint32, *uintptr) uintptr
var xcbKeyboardSyms func(uintptr) uintptr
var xcbKeyboardSymsLength func(uintptr) int32
var xcbSendEvent func(uintptr, uint8, uint32, uint32, *byte) uint32
var xcbResVersion func(uintptr, uint8, uint8) uint32
var xcbResVersionReply func(uintptr, uint32, *uintptr) uintptr
var xcbResQueryIDs func(uintptr, uint32, *xcbResIDSpec) uint32
var xcbResQueryIDsReply func(uintptr, uint32, *uintptr) uintptr
var xcbResIDsIterator func(uintptr) xcbScreenIterator
var xcbResIDValue func(uintptr) uintptr
var xcbResIDValueLength func(uintptr) int32
var xcbRequestCheck func(uintptr, uint32) uintptr
var xcbFlush func(uintptr) int32
var xcbFree func(uintptr)

type xcbScreenIterator struct {
	Data  uintptr
	Rem   int32
	Index int32
}

type xcbResIDSpec struct {
	Client uint32
	Mask   uint32
}

type x11Rect struct {
	X, Y, Width, Height, Border int32
}

type x11FrameIdentity struct {
	Root   uint32
	Window uint32
	Frame  uint32
	PID    uint32
	Rect   x11Rect
}

type x11Client struct {
	conn        uintptr
	root        uint32
	fd          int
	stopCancel  func() bool
	interrupted chan struct{}
}

func loadXCB() error {
	xcbOnce.Do(func() {
		var handles []uintptr
		for _, name := range []string{"libxcb.so.1", "libxcb-res.so.0", "libc.so.6"} {
			handle, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				xcbLoadErr = fmt.Errorf("%w: X11 dependency %s unavailable", ErrUnavailable, name)
				return
			}
			handles = append(handles, handle)
		}
		bindings := []struct {
			lib  uintptr
			name string
			out  any
		}{
			{handles[0], "xcb_connect", &xcbConnect},
			{handles[0], "xcb_disconnect", &xcbDisconnect},
			{handles[0], "xcb_connection_has_error", &xcbConnectionError},
			{handles[0], "xcb_get_file_descriptor", &xcbFileDescriptor},
			{handles[0], "xcb_get_setup", &xcbGetSetup},
			{handles[0], "xcb_setup_roots_iterator", &xcbRootsIterator},
			{handles[0], "xcb_screen_next", &xcbScreenNext},
			{handles[0], "xcb_intern_atom", &xcbInternAtom},
			{handles[0], "xcb_intern_atom_reply", &xcbInternAtomReply},
			{handles[0], "xcb_get_property", &xcbGetProperty},
			{handles[0], "xcb_get_property_reply", &xcbGetPropertyReply},
			{handles[0], "xcb_get_property_value", &xcbPropertyValue},
			{handles[0], "xcb_get_property_value_length", &xcbPropertyValueLength},
			{handles[0], "xcb_query_tree", &xcbQueryTree},
			{handles[0], "xcb_query_tree_reply", &xcbQueryTreeReply},
			{handles[0], "xcb_query_tree_children", &xcbTreeChildren},
			{handles[0], "xcb_query_tree_children_length", &xcbTreeChildrenLength},
			{handles[0], "xcb_get_geometry", &xcbGetGeometry},
			{handles[0], "xcb_get_geometry_reply", &xcbGetGeometryReply},
			{handles[0], "xcb_translate_coordinates", &xcbTranslateCoordinates},
			{handles[0], "xcb_translate_coordinates_reply", &xcbTranslateReply},
			{handles[0], "xcb_get_window_attributes", &xcbGetAttributes},
			{handles[0], "xcb_get_window_attributes_reply", &xcbGetAttributesReply},
			{handles[0], "xcb_get_input_focus", &xcbGetInputFocus},
			{handles[0], "xcb_get_input_focus_reply", &xcbGetInputFocusReply},
			{handles[0], "xcb_grab_pointer", &xcbGrabPointer},
			{handles[0], "xcb_grab_pointer_reply", &xcbGrabPointerReply},
			{handles[0], "xcb_ungrab_pointer_checked", &xcbUngrabPointer},
			{handles[0], "xcb_grab_keyboard", &xcbGrabKeyboard},
			{handles[0], "xcb_grab_keyboard_reply", &xcbGrabKeyboardReply},
			{handles[0], "xcb_ungrab_keyboard_checked", &xcbUngrabKeyboard},
			{handles[0], "xcb_query_pointer", &xcbQueryPointer},
			{handles[0], "xcb_query_pointer_reply", &xcbQueryPointerReply},
			{handles[0], "xcb_query_keymap", &xcbQueryKeymap},
			{handles[0], "xcb_query_keymap_reply", &xcbQueryKeymapReply},
			{handles[0], "xcb_get_keyboard_mapping", &xcbKeyboardMapping},
			{handles[0], "xcb_get_keyboard_mapping_reply", &xcbKeyboardMappingReply},
			{handles[0], "xcb_get_keyboard_mapping_keysyms", &xcbKeyboardSyms},
			{handles[0], "xcb_get_keyboard_mapping_keysyms_length", &xcbKeyboardSymsLength},
			{handles[0], "xcb_send_event_checked", &xcbSendEvent},
			{handles[0], "xcb_request_check", &xcbRequestCheck},
			{handles[0], "xcb_flush", &xcbFlush},
			{handles[1], "xcb_res_query_version", &xcbResVersion},
			{handles[1], "xcb_res_query_version_reply", &xcbResVersionReply},
			{handles[1], "xcb_res_query_client_ids", &xcbResQueryIDs},
			{handles[1], "xcb_res_query_client_ids_reply", &xcbResQueryIDsReply},
			{handles[1], "xcb_res_query_client_ids_ids_iterator", &xcbResIDsIterator},
			{handles[1], "xcb_res_client_id_value_value", &xcbResIDValue},
			{handles[1], "xcb_res_client_id_value_value_length", &xcbResIDValueLength},
			{handles[2], "free", &xcbFree},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(binding.lib, binding.name)
			if err != nil || address == 0 {
				xcbLoadErr = fmt.Errorf("%w: X11 symbol %s unavailable", ErrUnavailable, binding.name)
				return
			}
			purego.RegisterFunc(binding.out, address)
		}
	})
	return xcbLoadErr
}

func x11Session() bool {
	display := strings.TrimSpace(os.Getenv("DISPLAY"))
	return strings.EqualFold(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE")), "x11") && strings.HasPrefix(display, ":") && os.Getenv("WAYLAND_DISPLAY") == ""
}

func xcbBytes(pointer uintptr, length int) []byte {
	base := *(*unsafe.Pointer)(unsafe.Pointer(&pointer))
	return unsafe.Slice((*byte)(base), length)
}

func openX11(ctx context.Context) (*x11Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !x11Session() {
		return nil, ErrUnavailable
	}
	if err := loadXCB(); err != nil {
		return nil, err
	}
	type connectionResult struct {
		conn   uintptr
		screen int32
	}
	connected := make(chan connectionResult)
	go func() {
		var screen int32
		conn := xcbConnect(nil, &screen)
		select {
		case connected <- connectionResult{conn: conn, screen: screen}:
		case <-ctx.Done():
			if conn != 0 {
				xcbDisconnect(conn)
			}
		}
	}()
	var result connectionResult
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result = <-connected:
	}
	conn, screen := result.conn, result.screen
	if conn == 0 || xcbConnectionError(conn) != 0 || screen < 0 || screen > 31 {
		if conn != 0 {
			xcbDisconnect(conn)
		}
		return nil, errors.New("X11 connection unavailable")
	}
	setup := xcbGetSetup(conn)
	if setup == 0 {
		xcbDisconnect(conn)
		return nil, ErrUnavailable
	}
	iterator := xcbRootsIterator(setup)
	for i := int32(0); i < screen && iterator.Rem > 0; i++ {
		xcbScreenNext(&iterator)
	}
	if iterator.Rem <= 0 || iterator.Data == 0 {
		xcbDisconnect(conn)
		return nil, ErrUnavailable
	}
	root := binary.LittleEndian.Uint32(xcbBytes(iterator.Data, 4))
	if root == 0 {
		xcbDisconnect(conn)
		return nil, ErrUnavailable
	}
	fd := int(xcbFileDescriptor(conn))
	if fd < 0 {
		xcbDisconnect(conn)
		return nil, ErrUnavailable
	}
	c := &x11Client{conn: conn, root: root, fd: fd}
	c.armCancellation(ctx)
	if err := c.checkExtensions(); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *x11Client) close() {
	if c != nil && c.conn != 0 {
		c.disarmCancellation()
		xcbDisconnect(c.conn)
		c.conn = 0
	}
}

func (c *x11Client) armCancellation(ctx context.Context) {
	interrupted, fd := make(chan struct{}), c.fd
	c.interrupted = interrupted
	c.stopCancel = context.AfterFunc(ctx, func() {
		_ = syscall.Shutdown(fd, syscall.SHUT_RDWR)
		close(interrupted)
	})
}

func (c *x11Client) disarmCancellation() bool {
	if c.stopCancel == nil {
		return true
	}
	stopped := c.stopCancel()
	if !stopped {
		<-c.interrupted
	}
	c.stopCancel = nil
	return stopped
}

func (c *x11Client) reply(call func(uintptr, uint32, *uintptr) uintptr, cookie uint32) (uintptr, error) {
	var failure uintptr
	reply := call(c.conn, cookie, &failure)
	if failure != 0 {
		xcbFree(failure)
	}
	if failure != 0 || reply == 0 || xcbConnectionError(c.conn) != 0 {
		if reply != 0 {
			xcbFree(reply)
		}
		return 0, ErrTargetLost
	}
	return reply, nil
}

func (c *x11Client) checkExtensions() error {
	res, err := c.reply(xcbResVersionReply, xcbResVersion(c.conn, 1, 2))
	if err != nil {
		return err
	}
	resData := xcbBytes(res, 12)
	major, minor := binary.LittleEndian.Uint16(resData[8:10]), binary.LittleEndian.Uint16(resData[10:12])
	xcbFree(res)
	if major < 1 || (major == 1 && minor < 2) {
		return errors.New("X11 server does not provide local client PID verification")
	}
	return nil
}

func (c *x11Client) atom(name string) (uint32, error) {
	if len(name) == 0 || len(name) > 255 {
		return 0, ErrUnavailable
	}
	cookie := xcbInternAtom(c.conn, 1, uint16(len(name)), portalCString(name))
	reply, err := c.reply(xcbInternAtomReply, cookie)
	if err != nil {
		return 0, err
	}
	defer xcbFree(reply)
	atom := binary.LittleEndian.Uint32(xcbBytes(reply, 12)[8:12])
	if atom == 0 {
		return 0, ErrUnavailable
	}
	return atom, nil
}

func (c *x11Client) property(window, atom, kind uint32, limit int) ([]byte, uint8, error) {
	if limit <= 0 || limit > 32768 {
		return nil, 0, ErrNotAllowed
	}
	cookie := xcbGetProperty(c.conn, 0, window, atom, kind, 0, uint32((limit+3)/4))
	reply, err := c.reply(xcbGetPropertyReply, cookie)
	if err != nil {
		return nil, 0, err
	}
	defer xcbFree(reply)
	header := xcbBytes(reply, 32)
	format := header[1]
	if binary.LittleEndian.Uint32(header[8:12]) != kind || binary.LittleEndian.Uint32(header[12:16]) != 0 {
		return nil, 0, ErrUnavailable
	}
	length := int(xcbPropertyValueLength(reply))
	if length <= 0 || length > limit {
		return nil, 0, ErrUnavailable
	}
	pointer := xcbPropertyValue(reply)
	if pointer == 0 {
		return nil, 0, ErrUnavailable
	}
	return append([]byte(nil), xcbBytes(pointer, length)...), format, nil
}

func (c *x11Client) tree(window uint32) (uint32, uint32, []uint32, error) {
	reply, err := c.reply(xcbQueryTreeReply, xcbQueryTree(c.conn, window))
	if err != nil {
		return 0, 0, nil, err
	}
	defer xcbFree(reply)
	header := xcbBytes(reply, 32)
	root, parent := binary.LittleEndian.Uint32(header[8:12]), binary.LittleEndian.Uint32(header[12:16])
	count := int(xcbTreeChildrenLength(reply))
	if count < 0 || count > 8192 {
		return 0, 0, nil, ErrUnavailable
	}
	children := make([]uint32, count)
	if count > 0 {
		pointer := xcbTreeChildren(reply)
		if pointer == 0 {
			return 0, 0, nil, ErrUnavailable
		}
		data := xcbBytes(pointer, count*4)
		for i := range children {
			children[i] = binary.LittleEndian.Uint32(data[i*4:])
		}
	}
	return root, parent, children, nil
}

func (c *x11Client) ancestor(window uint32) (uint32, error) {
	seen := make(map[uint32]bool)
	for depth := 0; depth < 32 && window != 0 && !seen[window]; depth++ {
		seen[window] = true
		root, parent, _, err := c.tree(window)
		if err != nil || root != c.root || parent == 0 {
			return 0, ErrTargetLost
		}
		if parent == c.root {
			return window, nil
		}
		window = parent
	}
	return 0, ErrTargetLost
}

func (c *x11Client) geometry(window uint32) (x11Rect, error) {
	reply, err := c.reply(xcbGetGeometryReply, xcbGetGeometry(c.conn, window))
	if err != nil {
		return x11Rect{}, err
	}
	header := xcbBytes(reply, 24)
	root := binary.LittleEndian.Uint32(header[8:12])
	width, height := int32(binary.LittleEndian.Uint16(header[16:18])), int32(binary.LittleEndian.Uint16(header[18:20]))
	border := int32(binary.LittleEndian.Uint16(header[20:22]))
	xcbFree(reply)
	if root != c.root || width <= 0 || height <= 0 {
		return x11Rect{}, ErrTargetLost
	}
	reply, err = c.reply(xcbTranslateReply, xcbTranslateCoordinates(c.conn, window, c.root, 0, 0))
	if err != nil {
		return x11Rect{}, err
	}
	defer xcbFree(reply)
	translate := xcbBytes(reply, 16)
	if translate[1] == 0 {
		return x11Rect{}, ErrTargetLost
	}
	return x11Rect{X: int32(int16(binary.LittleEndian.Uint16(translate[12:14]))), Y: int32(int16(binary.LittleEndian.Uint16(translate[14:16]))), Width: width, Height: height, Border: border}, nil
}

func (c *x11Client) visible(window uint32) (bool, error) {
	reply, err := c.reply(xcbGetAttributesReply, xcbGetAttributes(c.conn, window))
	if err != nil {
		return false, err
	}
	defer xcbFree(reply)
	return xcbBytes(reply, 28)[26] == 2, nil // XCB_MAP_STATE_VIEWABLE
}

// XRes obtains the local PID from the X server, not from the forgeable
// _NET_WM_PID property supplied by the client application.
func (c *x11Client) ownerPID(window uint32) (uint32, error) {
	spec := xcbResIDSpec{Client: window, Mask: 2} // LOCAL_CLIENT_PID
	reply, err := c.reply(xcbResQueryIDsReply, xcbResQueryIDs(c.conn, 1, &spec))
	if err != nil {
		return 0, err
	}
	defer xcbFree(reply)
	iterator := xcbResIDsIterator(reply)
	if iterator.Rem != 1 || iterator.Data == 0 || xcbResIDValueLength(iterator.Data) != 1 {
		return 0, ErrTargetLost
	}
	header := xcbBytes(iterator.Data, 12)
	if binary.LittleEndian.Uint32(header[4:8]) != 2 {
		return 0, ErrTargetLost
	}
	value := xcbResIDValue(iterator.Data)
	if value == 0 {
		return 0, ErrTargetLost
	}
	pid := binary.LittleEndian.Uint32(xcbBytes(value, 4))
	if pid == 0 {
		return 0, ErrTargetLost
	}
	return pid, nil
}

func (c *x11Client) identify(window atspiWindow) (x11FrameIdentity, error) {
	listAtom, err := c.atom("_NET_CLIENT_LIST_STACKING")
	if err != nil {
		return x11FrameIdentity{}, err
	}
	pidAtom, err := c.atom("_NET_WM_PID")
	if err != nil {
		return x11FrameIdentity{}, err
	}
	nameAtom, err := c.atom("_NET_WM_NAME")
	if err != nil {
		return x11FrameIdentity{}, err
	}
	utf8Atom, err := c.atom("UTF8_STRING")
	if err != nil {
		return x11FrameIdentity{}, err
	}
	list, format, err := c.property(c.root, listAtom, 33, 32768) // WINDOW
	if err != nil || format != 32 || len(list)%4 != 0 || window.title == "" {
		return x11FrameIdentity{}, ErrUnavailable
	}
	var match uint32
	for i := 0; i < len(list); i += 4 {
		candidate := binary.LittleEndian.Uint32(list[i:])
		pid, pidFormat, err := c.property(candidate, pidAtom, 6, 4) // CARDINAL
		if err != nil || pidFormat != 32 || len(pid) != 4 || int(binary.LittleEndian.Uint32(pid)) != window.pid {
			continue
		}
		owner, err := c.ownerPID(candidate)
		if err != nil || int(owner) != window.pid {
			continue
		}
		name, nameFormat, err := c.property(candidate, nameAtom, utf8Atom, 4096)
		if err != nil || nameFormat != 8 || string(name) != window.title {
			continue
		}
		visible, err := c.visible(candidate)
		if err != nil || !visible {
			continue
		}
		if match != 0 {
			return x11FrameIdentity{}, errors.New("X11 target is ambiguous")
		}
		match = candidate
	}
	if match == 0 {
		return x11FrameIdentity{}, ErrTargetLost
	}
	frame, err := c.ancestor(match)
	if err != nil {
		return x11FrameIdentity{}, err
	}
	rect, err := c.geometry(match)
	if err != nil {
		return x11FrameIdentity{}, err
	}
	return x11FrameIdentity{Root: c.root, Window: match, Frame: frame, PID: uint32(window.pid), Rect: rect}, nil
}

func (c *x11Client) focused(identity x11FrameIdentity) error {
	_, err := c.focusedWindow(identity)
	return err
}

func (c *x11Client) focusedWindow(identity x11FrameIdentity) (uint32, error) {
	activeAtom, err := c.atom("_NET_ACTIVE_WINDOW")
	if err != nil {
		return 0, err
	}
	active, format, err := c.property(c.root, activeAtom, 33, 4)
	if err != nil || format != 32 || len(active) != 4 || binary.LittleEndian.Uint32(active) != identity.Window {
		return 0, ErrTargetLost
	}
	reply, err := c.reply(xcbGetInputFocusReply, xcbGetInputFocus(c.conn))
	if err != nil {
		return 0, err
	}
	focus := binary.LittleEndian.Uint32(xcbBytes(reply, 12)[8:12])
	xcbFree(reply)
	owner, err := c.ownerPID(focus)
	if err != nil || owner != identity.PID {
		return 0, ErrTargetLost
	}
	destination := focus
	seen := make(map[uint32]bool)
	for depth := 0; depth < 32 && focus > 1 && !seen[focus]; depth++ {
		if focus == identity.Window {
			return destination, nil
		}
		seen[focus] = true
		root, parent, _, err := c.tree(focus)
		if err != nil || root != c.root || parent == c.root {
			break
		}
		focus = parent
	}
	return 0, ErrTargetLost
}

func (c *x11Client) unobscured(identity x11FrameIdentity, x, y int32) error {
	root, _, children, err := c.tree(c.root)
	if err != nil || root != c.root {
		return ErrTargetLost
	}
	index := -1
	for i, child := range children {
		if child == identity.Frame {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrTargetLost
	}
	for _, child := range children[index+1:] {
		visible, err := c.visible(child)
		if err != nil {
			return err
		}
		if !visible {
			continue
		}
		rect, err := c.geometry(child)
		if err != nil {
			return err
		}
		if int64(x) >= int64(rect.X)-int64(rect.Border) && int64(y) >= int64(rect.Y)-int64(rect.Border) && int64(x) < int64(rect.X)+int64(rect.Width)+int64(rect.Border) && int64(y) < int64(rect.Y)+int64(rect.Height)+int64(rect.Border) {
			return ErrNotAllowed
		}
	}
	return nil
}

func (c *x11Client) idleInput() error {
	keyReply, err := c.reply(xcbQueryKeymapReply, xcbQueryKeymap(c.conn))
	if err != nil {
		return err
	}
	keys := append([]byte(nil), xcbBytes(keyReply, 40)[8:40]...)
	xcbFree(keyReply)
	for _, key := range keys {
		if key != 0 {
			return fmt.Errorf("%w: release held keys before X11 input", ErrNotAllowed)
		}
	}
	pointer, err := c.reply(xcbQueryPointerReply, xcbQueryPointer(c.conn, c.root))
	if err != nil {
		return err
	}
	defer xcbFree(pointer)
	data := xcbBytes(pointer, 26)
	if data[1] == 0 || binary.LittleEndian.Uint16(data[24:26]) != 0 {
		return fmt.Errorf("%w: release buttons and disable latched modifiers before X11 input", ErrNotAllowed)
	}
	return nil
}

type x11EventTarget struct {
	Window uint32
	X, Y   int16
}

// Follow the X server's actual child hit-test and require every destination to
// belong to the selected process. Embedded foreign clients are not targets.
func (c *x11Client) pointTarget(identity x11FrameIdentity, x, y int32) (x11EventTarget, error) {
	if x < -32768 || x > 32767 || y < -32768 || y > 32767 {
		return x11EventTarget{}, ErrNotAllowed
	}
	window := identity.Window
	seen := make(map[uint32]bool)
	for depth := 0; depth < 32 && window > 1 && !seen[window]; depth++ {
		seen[window] = true
		owner, err := c.ownerPID(window)
		if err != nil || owner != identity.PID {
			return x11EventTarget{}, ErrTargetLost
		}
		visible, err := c.visible(window)
		if err != nil || !visible {
			return x11EventTarget{}, ErrTargetLost
		}
		reply, err := c.reply(xcbTranslateReply, xcbTranslateCoordinates(c.conn, c.root, window, int16(x), int16(y)))
		if err != nil {
			return x11EventTarget{}, err
		}
		data := xcbBytes(reply, 16)
		sameScreen := data[1] != 0
		child := binary.LittleEndian.Uint32(data[8:12])
		localX := int16(binary.LittleEndian.Uint16(data[12:14]))
		localY := int16(binary.LittleEndian.Uint16(data[14:16]))
		xcbFree(reply)
		if !sameScreen || localX < 0 || localY < 0 {
			return x11EventTarget{}, ErrTargetLost
		}
		if child == 0 {
			return x11EventTarget{Window: window, X: localX, Y: localY}, nil
		}
		window = child
	}
	return x11EventTarget{}, ErrTargetLost
}

// Probe a pre-existing active grab and immediately release our temporary grab.
// Disconnect also releases it if a canceled connection cannot send ungrab.
func (c *x11Client) noActiveGrab(ctx context.Context, identity x11FrameIdentity, pointer bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var reply uintptr
	var err error
	if pointer {
		cookie := xcbGrabPointer(c.conn, 0, identity.Window, 0, 1, 1, 0, 0, 0)
		reply, err = c.reply(xcbGrabPointerReply, cookie)
	} else {
		cookie := xcbGrabKeyboard(c.conn, 0, identity.Window, 0, 1, 1)
		reply, err = c.reply(xcbGrabKeyboardReply, cookie)
	}
	if err != nil {
		return err
	}
	status := xcbBytes(reply, 2)[1]
	xcbFree(reply)
	if status != 0 {
		return ErrNotAllowed
	}
	var ungrab uint32
	if pointer {
		ungrab = xcbUngrabPointer(c.conn, 0)
	} else {
		ungrab = xcbUngrabKeyboard(c.conn, 0)
	}
	if err := c.checked(ungrab); err != nil {
		return err
	}
	return ctx.Err()
}

func (c *x11Client) checked(cookie uint32) error {
	if cookie == 0 {
		return ErrUnavailable
	}
	failure := xcbRequestCheck(c.conn, cookie)
	if failure != 0 {
		xcbFree(failure)
		return ErrTargetLost
	}
	if xcbConnectionError(c.conn) != 0 {
		return ErrTargetLost
	}
	return nil
}

// SendEvent with NoEventMask addresses only the verified window's creator.
// Unlike global XTEST input it cannot be redirected by a passive grab.
// Applications may ignore synthetic events; dispatch is not proof of effect.
func (c *x11Client) sendPair(ctx context.Context, target x11EventTarget, rootX, rootY int16, pressType, releaseType, detail uint8) error {
	if target.Window <= 1 || detail == 0 || !((pressType == 2 && releaseType == 3) || (pressType == 4 && releaseType == 5)) {
		return ErrNotAllowed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.disarmCancellation() {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrTargetLost
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Once dispatch starts, queue and flush release before allowing cancellation
	// to shut down the socket. An already-dispatched press cannot be undone.
	var releaseState uint16
	if pressType == 4 && detail <= 5 {
		releaseState = 1 << (7 + detail)
	}
	pressEvent := x11CoreEvent(pressType, detail, c.root, target.Window, rootX, rootY, target.X, target.Y, 0)
	releaseEvent := x11CoreEvent(releaseType, detail, c.root, target.Window, rootX, rootY, target.X, target.Y, releaseState)
	press := xcbSendEvent(c.conn, 0, target.Window, 0, &pressEvent[0])
	release := xcbSendEvent(c.conn, 0, target.Window, 0, &releaseEvent[0])
	flushed := xcbFlush(c.conn)
	c.armCancellation(ctx)
	if flushed <= 0 {
		return ErrTargetLost
	}
	pressErr := c.checked(press)
	releaseErr := c.checked(release)
	if pressErr != nil || releaseErr != nil {
		return errors.New("X11 input dispatch failed or may be incomplete")
	}
	return nil
}

func x11KeySym(key string) (uint32, bool) {
	switch key {
	case "enter":
		return 0xff0d, true
	case "tab":
		return 0xff09, true
	case "escape":
		return 0xff1b, true
	case "backspace":
		return 0xff08, true
	case "delete":
		return 0xffff, true
	case "up":
		return 0xff52, true
	case "down":
		return 0xff54, true
	case "left":
		return 0xff51, true
	case "right":
		return 0xff53, true
	case "page_up":
		return 0xff55, true
	case "page_down":
		return 0xff56, true
	case "home":
		return 0xff50, true
	case "end":
		return 0xff57, true
	case "space":
		return 0x20, true
	default:
		return 0, false
	}
}

func (c *x11Client) keycode(key string) (uint8, error) {
	symbol, ok := x11KeySym(key)
	if !ok {
		return 0, ErrNotAllowed
	}
	setup := xcbGetSetup(c.conn)
	if setup == 0 {
		return 0, ErrTargetLost
	}
	header := xcbBytes(setup, 36)
	minimum, maximum := int(header[34]), int(header[35])
	count := maximum - minimum + 1
	if minimum < 8 || maximum > 255 || count < 1 || count > 255 {
		return 0, ErrUnavailable
	}
	reply, err := c.reply(xcbKeyboardMappingReply, xcbKeyboardMapping(c.conn, uint8(minimum), uint8(count)))
	if err != nil {
		return 0, err
	}
	defer xcbFree(reply)
	perKey := int(xcbBytes(reply, 2)[1])
	length := int(xcbKeyboardSymsLength(reply))
	pointer := xcbKeyboardSyms(reply)
	if perKey <= 0 || perKey > 64 || length != count*perKey || pointer == 0 {
		return 0, ErrUnavailable
	}
	data := xcbBytes(pointer, length*4)
	var match uint8
	for i := 0; i < count; i++ {
		// Only an unmodified primary symbol is eligible. Never synthesize a
		// guessed modifier or alternate keyboard-layout group.
		if binary.LittleEndian.Uint32(data[i*perKey*4:]) == symbol {
			if match != 0 {
				return 0, errors.New("X11 key mapping is ambiguous")
			}
			match = uint8(minimum + i)
		}
	}
	if match == 0 {
		return 0, ErrUnavailable
	}
	return match, nil
}
