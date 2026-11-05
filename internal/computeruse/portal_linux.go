//go:build linux && (amd64 || arm64)

package computeruse

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Portal and GStreamer are loaded at runtime: a missing service or plugin is
// an explicit unavailable capability, not permission to capture the desktop.
var portalOnce sync.Once
var portalErr error
var portalBusGet func(int32, uintptr, *uintptr) uintptr
var portalUniqueName func(uintptr) uintptr
var portalCall func(uintptr, *byte, *byte, *byte, *byte, uintptr, uintptr, int32, int32, uintptr, *uintptr) uintptr
var portalCallFD func(uintptr, *byte, *byte, *byte, *byte, uintptr, uintptr, int32, int32, uintptr, *uintptr, uintptr, *uintptr) uintptr
var portalSubscribe func(uintptr, *byte, *byte, *byte, *byte, *byte, int32, uintptr, uintptr, uintptr) uint32
var portalUnsubscribe func(uintptr, uint32)
var portalFDGet func(uintptr, int32, *uintptr) int32
var portalContextNew func() uintptr
var portalContextUnref func(uintptr)
var portalContextPush func(uintptr)
var portalContextPop func(uintptr)
var portalContextIterate func(uintptr, int32) int32
var portalVariantTypeNew func(*byte) uintptr
var portalVariantTypeFree func(uintptr)
var portalVariantParse func(uintptr, *byte, uintptr, uintptr, *uintptr) uintptr
var portalVariantRef func(uintptr) uintptr
var portalVariantUnref func(uintptr)
var portalVariantChild func(uintptr, uintptr) uintptr
var portalVariantCount func(uintptr) uintptr
var portalVariantLookup func(uintptr, *byte, uintptr) uintptr
var portalVariantString func(uintptr, *uintptr) uintptr
var portalVariantUint32 func(uintptr) uint32
var portalVariantUint64 func(uintptr) uint64
var portalVariantHandle func(uintptr) int32
var portalObjectUnref func(uintptr)
var portalErrorFree func(uintptr)
var portalGSTInit func(uintptr, uintptr)
var portalGSTParse func(*byte, *uintptr) uintptr
var portalGSTGetByName func(uintptr, *byte) uintptr
var portalGSTSetState func(uintptr, int32) int32
var portalGSTUnref func(uintptr)
var portalGSTPull func(uintptr, uint64) uintptr
var portalGSTSampleBuffer func(uintptr) uintptr
var portalGSTBufferSize func(uintptr) uintptr
var portalGSTBufferExtract func(uintptr, uintptr, *byte, uintptr) uintptr
var portalGSTSampleUnref func(uintptr)
var portalCallback uintptr
var portalClosedCallback uintptr
var portalCallbacks sync.Map
var portalClosedSessions sync.Map
var portalCallbackID atomic.Uint64

func portalCString(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

func portalBorrowedString(p uintptr) string {
	if p == 0 {
		return ""
	}
	base := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	var data []byte
	for i := 0; i < 4096; i++ {
		value := *(*byte)(unsafe.Add(base, i))
		if value == 0 {
			return string(data)
		}
		data = append(data, value)
	}
	return ""
}

func portalSignal(_, _, _, _, _, parameters, data uintptr) {
	entry, ok := portalCallbacks.Load(data)
	if !ok || parameters == 0 {
		return
	}
	value := portalVariantRef(parameters)
	select {
	case entry.(chan uintptr) <- value:
	default:
		portalVariantUnref(value)
	}
}

func portalSessionClosed(_, _, _, _, _, _, data uintptr) {
	if entry, ok := portalClosedSessions.Load(data); ok {
		entry.(*linuxPortal).revoked.Store(true)
	}
}

func loadLinuxPortal() error {
	portalOnce.Do(func() {
		var libraries []uintptr
		for _, name := range []string{"libgio-2.0.so.0", "libglib-2.0.so.0", "libgobject-2.0.so.0", "libgstreamer-1.0.so.0", "libgstapp-1.0.so.0"} {
			handle, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				portalErr = fmt.Errorf("%w: Linux portal dependency %s unavailable", ErrUnavailable, name)
				return
			}
			libraries = append(libraries, handle)
		}
		bindings := []struct {
			library uintptr
			name    string
			out     any
		}{
			{libraries[0], "g_bus_get_sync", &portalBusGet},
			{libraries[0], "g_dbus_connection_get_unique_name", &portalUniqueName},
			{libraries[0], "g_dbus_connection_call_sync", &portalCall},
			{libraries[0], "g_dbus_connection_call_with_unix_fd_list_sync", &portalCallFD},
			{libraries[0], "g_dbus_connection_signal_subscribe", &portalSubscribe},
			{libraries[0], "g_dbus_connection_signal_unsubscribe", &portalUnsubscribe},
			{libraries[0], "g_unix_fd_list_get", &portalFDGet},
			{libraries[1], "g_main_context_new", &portalContextNew},
			{libraries[1], "g_main_context_unref", &portalContextUnref},
			{libraries[1], "g_main_context_push_thread_default", &portalContextPush},
			{libraries[1], "g_main_context_pop_thread_default", &portalContextPop},
			{libraries[1], "g_main_context_iteration", &portalContextIterate},
			{libraries[1], "g_variant_type_new", &portalVariantTypeNew},
			{libraries[1], "g_variant_type_free", &portalVariantTypeFree},
			{libraries[1], "g_variant_parse", &portalVariantParse},
			{libraries[1], "g_variant_ref", &portalVariantRef},
			{libraries[1], "g_variant_unref", &portalVariantUnref},
			{libraries[1], "g_variant_get_child_value", &portalVariantChild},
			{libraries[1], "g_variant_n_children", &portalVariantCount},
			{libraries[1], "g_variant_lookup_value", &portalVariantLookup},
			{libraries[1], "g_variant_get_string", &portalVariantString},
			{libraries[1], "g_variant_get_uint32", &portalVariantUint32},
			{libraries[1], "g_variant_get_uint64", &portalVariantUint64},
			{libraries[1], "g_variant_get_handle", &portalVariantHandle},
			{libraries[1], "g_error_free", &portalErrorFree},
			{libraries[2], "g_object_unref", &portalObjectUnref},
			{libraries[3], "gst_init", &portalGSTInit},
			{libraries[3], "gst_parse_launch", &portalGSTParse},
			{libraries[3], "gst_bin_get_by_name", &portalGSTGetByName},
			{libraries[3], "gst_element_set_state", &portalGSTSetState},
			{libraries[3], "gst_object_unref", &portalGSTUnref},
			{libraries[3], "gst_sample_get_buffer", &portalGSTSampleBuffer},
			{libraries[3], "gst_buffer_get_size", &portalGSTBufferSize},
			{libraries[3], "gst_buffer_extract", &portalGSTBufferExtract},
			{libraries[3], "gst_mini_object_unref", &portalGSTSampleUnref},
			{libraries[4], "gst_app_sink_try_pull_sample", &portalGSTPull},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(binding.library, binding.name)
			if err != nil || address == 0 {
				portalErr = fmt.Errorf("%w: Linux portal symbol %s unavailable", ErrUnavailable, binding.name)
				return
			}
			purego.RegisterFunc(binding.out, address)
		}
		portalCallback = purego.NewCallback(portalSignal)
		portalClosedCallback = purego.NewCallback(portalSessionClosed)
		portalGSTInit(0, 0)
	})
	return portalErr
}

type linuxPortal struct {
	mu       sync.Mutex
	conn     uintptr
	main     uintptr
	session  string
	stream   uint32
	serial   uint64
	fd       int
	pipeline uintptr
	sink     uintptr
	revoked  atomic.Bool
	closeSub uint32
	closeID  uintptr
	closed   bool
}

func portalFailure(p uintptr) error {
	if p == 0 {
		return nil
	}
	portalErrorFree(p)
	return errors.New("desktop portal request failed")
}

func portalVariant(signature, text string) (uintptr, error) {
	typ := portalVariantTypeNew(portalCString(signature))
	if typ == 0 {
		return 0, ErrUnavailable
	}
	defer portalVariantTypeFree(typ)
	var failure uintptr
	value := portalVariantParse(typ, portalCString(text), 0, 0, &failure)
	if portalFailure(failure) != nil || value == 0 {
		return 0, errors.New("invalid desktop portal request")
	}
	return value, nil
}

func portalToken() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "golder" + hex.EncodeToString(nonce[:]), nil
}

func (p *linuxPortal) request(ctx context.Context, iface, method, signature string, params func(string) string) (uintptr, error) {
	token, err := portalToken()
	if err != nil {
		return 0, err
	}
	name := portalBorrowedString(portalUniqueName(p.conn))
	if !strings.HasPrefix(name, ":") {
		return 0, ErrUnavailable
	}
	path := "/org/freedesktop/portal/desktop/request/" + strings.ReplaceAll(name[1:], ".", "_") + "/" + token
	response := make(chan uintptr, 1)
	id := uintptr(portalCallbackID.Add(1))
	portalCallbacks.Store(id, response)
	subscription := portalSubscribe(p.conn, portalCString("org.freedesktop.portal.Desktop"), portalCString("org.freedesktop.portal.Request"), portalCString("Response"), portalCString(path), nil, 0, portalCallback, id, 0)
	defer func() {
		portalUnsubscribe(p.conn, subscription)
		portalCallbacks.Delete(id)
		select {
		case value := <-response:
			portalVariantUnref(value)
		default:
		}
	}()
	if subscription == 0 {
		return 0, ErrUnavailable
	}
	value, err := portalVariant(signature, params(token))
	if err != nil {
		return 0, err
	}
	defer portalVariantUnref(value)
	var failure uintptr
	reply := portalCall(p.conn, portalCString("org.freedesktop.portal.Desktop"), portalCString("/org/freedesktop/portal/desktop"), portalCString(iface), portalCString(method), value, 0, 0, 5000, 0, &failure)
	if portalFailure(failure) != nil || reply == 0 {
		return 0, errors.New("desktop portal call failed")
	}
	defer portalVariantUnref(reply)
	if portalVariantCount(reply) != 1 {
		return 0, ErrUnavailable
	}
	handle := portalVariantChild(reply, 0)
	if handle == 0 {
		return 0, ErrUnavailable
	}
	actual := portalBorrowedString(portalVariantString(handle, nil))
	portalVariantUnref(handle)
	if actual != path {
		return 0, errors.New("desktop portal returned an unexpected request identity")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		for i := 0; i < 64 && portalContextIterate(p.main, 0) != 0; i++ {
		}
		if p.revoked.Load() {
			return 0, ErrTargetLost
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case parameters := <-response:
			if portalVariantCount(parameters) != 2 {
				portalVariantUnref(parameters)
				return 0, ErrUnavailable
			}
			code := portalVariantChild(parameters, 0)
			results := portalVariantChild(parameters, 1)
			if code == 0 || results == 0 {
				if code != 0 {
					portalVariantUnref(code)
				}
				if results != 0 {
					portalVariantUnref(results)
				}
				portalVariantUnref(parameters)
				return 0, ErrUnavailable
			}
			status := portalVariantUint32(code)
			portalVariantUnref(code)
			portalVariantUnref(parameters)
			if status != 0 {
				portalVariantUnref(results)
				return 0, errors.New("desktop portal selection was canceled or denied")
			}
			return results, nil
		case <-ticker.C:
		}
	}
}

func portalObjectPath(s string) bool {
	if !strings.HasPrefix(s, "/org/freedesktop/portal/desktop/session/") {
		return false
	}
	for _, c := range s {
		if c != '/' && c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func openLinuxPortal(ctx context.Context) (*linuxPortal, error) {
	if err := loadLinuxPortal(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p := &linuxPortal{fd: -1}
	p.main = portalContextNew()
	if p.main == 0 {
		return nil, ErrUnavailable
	}
	portalContextPush(p.main)
	defer portalContextPop(p.main)
	var failure uintptr
	p.conn = portalBusGet(2, 0, &failure) // G_BUS_TYPE_SESSION
	if portalFailure(failure) != nil || p.conn == 0 {
		p.close()
		return nil, errors.New("desktop portal session bus unavailable")
	}
	sessionToken, err := portalToken()
	if err != nil {
		p.close()
		return nil, err
	}
	created, err := p.request(ctx, "org.freedesktop.portal.ScreenCast", "CreateSession", "(a{sv})", func(token string) string {
		return fmt.Sprintf("({'handle_token': <'%s'>, 'session_handle_token': <'%s'>},)", token, sessionToken)
	})
	if err != nil {
		p.close()
		return nil, err
	}
	handle := portalVariantLookup(created, portalCString("session_handle"), 0)
	if handle != 0 {
		p.session = portalBorrowedString(portalVariantString(handle, nil))
		portalVariantUnref(handle)
	}
	portalVariantUnref(created)
	if !portalObjectPath(p.session) {
		p.close()
		return nil, errors.New("desktop portal returned an invalid session")
	}
	p.closeID = uintptr(portalCallbackID.Add(1))
	portalClosedSessions.Store(p.closeID, p)
	p.closeSub = portalSubscribe(p.conn, portalCString("org.freedesktop.portal.Desktop"), portalCString("org.freedesktop.portal.Session"), portalCString("Closed"), portalCString(p.session), nil, 0, portalClosedCallback, p.closeID, 0)
	if p.closeSub == 0 {
		p.close()
		return nil, errors.New("desktop portal session monitoring unavailable")
	}
	selected, err := p.request(ctx, "org.freedesktop.portal.ScreenCast", "SelectSources", "(oa{sv})", func(token string) string {
		return fmt.Sprintf("('%s', {'handle_token': <'%s'>, 'types': <uint32 2>, 'multiple': <false>})", p.session, token)
	})
	if err != nil {
		p.close()
		return nil, err
	}
	portalVariantUnref(selected)
	started, err := p.request(ctx, "org.freedesktop.portal.ScreenCast", "Start", "(osa{sv})", func(token string) string {
		return fmt.Sprintf("('%s', '', {'handle_token': <'%s'>})", p.session, token)
	})
	if err != nil {
		p.close()
		return nil, err
	}
	streams := portalVariantLookup(started, portalCString("streams"), 0)
	if streams == 0 || portalVariantCount(streams) != 1 {
		if streams != 0 {
			portalVariantUnref(streams)
		}
		portalVariantUnref(started)
		p.close()
		return nil, errors.New("desktop portal did not return one window stream")
	}
	stream := portalVariantChild(streams, 0)
	if stream == 0 || portalVariantCount(stream) != 2 {
		if stream != 0 {
			portalVariantUnref(stream)
		}
		portalVariantUnref(streams)
		portalVariantUnref(started)
		p.close()
		return nil, errors.New("desktop portal returned an invalid window stream")
	}
	id := portalVariantChild(stream, 0)
	props := portalVariantChild(stream, 1)
	var sourceType uintptr
	if props != 0 {
		sourceType = portalVariantLookup(props, portalCString("source_type"), 0)
	}
	if id != 0 && sourceType != 0 && portalVariantUint32(sourceType) == 2 {
		p.stream = portalVariantUint32(id)
		serial := portalVariantLookup(props, portalCString("pipewire-serial"), 0)
		if serial != 0 {
			p.serial = portalVariantUint64(serial)
			portalVariantUnref(serial)
		}
	}
	for _, value := range []uintptr{sourceType, props, id, stream, streams, started} {
		if value != 0 {
			portalVariantUnref(value)
		}
	}
	if p.stream == 0 || p.serial == 0 {
		p.close()
		return nil, errors.New("desktop portal did not provide a stable window stream identity")
	}
	if err := p.openPipeWire(ctx); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (p *linuxPortal) openPipeWire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value, err := portalVariant("(oa{sv})", fmt.Sprintf("('%s', {})", p.session))
	if err != nil {
		return err
	}
	defer portalVariantUnref(value)
	var fdList, failure uintptr
	reply := portalCallFD(p.conn, portalCString("org.freedesktop.portal.Desktop"), portalCString("/org/freedesktop/portal/desktop"), portalCString("org.freedesktop.portal.ScreenCast"), portalCString("OpenPipeWireRemote"), value, 0, 0, 5000, 0, &fdList, 0, &failure)
	if portalFailure(failure) != nil || reply == 0 || fdList == 0 {
		if reply != 0 {
			portalVariantUnref(reply)
		}
		if fdList != 0 {
			portalObjectUnref(fdList)
		}
		return errors.New("desktop portal PipeWire connection unavailable")
	}
	defer portalVariantUnref(reply)
	defer portalObjectUnref(fdList)
	if portalVariantCount(reply) != 1 {
		return ErrUnavailable
	}
	index := portalVariantChild(reply, 0)
	if index == 0 {
		return ErrUnavailable
	}
	handle := portalVariantHandle(index)
	portalVariantUnref(index)
	failure = 0
	fd := portalFDGet(fdList, handle, &failure)
	if portalFailure(failure) != nil || fd < 0 {
		return errors.New("desktop portal PipeWire descriptor unavailable")
	}
	p.fd = int(fd)
	description := fmt.Sprintf("pipewiresrc fd=%d target-object=%d dont-reconnect=true ! videoconvert ! pngenc ! appsink name=golder_sink sync=false max-buffers=1 drop=true", p.fd, p.serial)
	failure = 0
	p.pipeline = portalGSTParse(portalCString(description), &failure)
	if portalFailure(failure) != nil || p.pipeline == 0 {
		return errors.New("PipeWire window capture pipeline unavailable")
	}
	p.sink = portalGSTGetByName(p.pipeline, portalCString("golder_sink"))
	if p.sink == 0 || portalGSTSetState(p.pipeline, 2) == 0 {
		return errors.New("PipeWire window capture could not initialize")
	}
	return nil
}

func (p *linuxPortal) frame(ctx context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.sink == 0 {
		return nil, ErrTargetLost
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if portalGSTSetState(p.pipeline, 4) == 0 {
		return nil, errors.New("PipeWire window capture could not start")
	}
	// Do not keep a continuous stream running between explicit frame requests.
	defer portalGSTSetState(p.pipeline, 2)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i := 0; i < 64 && portalContextIterate(p.main, 0) != 0; i++ {
		}
		if p.revoked.Load() {
			return nil, ErrTargetLost
		}
		sample := portalGSTPull(p.sink, uint64(200*time.Millisecond))
		if sample == 0 {
			continue
		}
		buffer := portalGSTSampleBuffer(sample)
		if buffer == 0 {
			portalGSTSampleUnref(sample)
			return nil, errors.New("PipeWire window frame is empty")
		}
		size := portalGSTBufferSize(buffer)
		if size == 0 || size > MaxFrameBytes {
			portalGSTSampleUnref(sample)
			return nil, errors.New("PipeWire window frame is empty or too large")
		}
		data := make([]byte, int(size))
		copied := portalGSTBufferExtract(buffer, 0, &data[0], size)
		portalGSTSampleUnref(sample)
		if copied != size {
			return nil, errors.New("PipeWire window frame could not be read")
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > MaxFramePixels {
			return nil, errors.New("PipeWire window frame is invalid or too large")
		}
		for i := 0; i < 64 && portalContextIterate(p.main, 0) != 0; i++ {
		}
		if p.revoked.Load() {
			return nil, ErrTargetLost
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return data, nil
	}
}

func (p *linuxPortal) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if p.closeSub != 0 {
		portalUnsubscribe(p.conn, p.closeSub)
	}
	if p.closeID != 0 {
		portalClosedSessions.Delete(p.closeID)
	}
	if p.pipeline != 0 {
		portalGSTSetState(p.pipeline, 1)
	}
	if p.sink != 0 {
		portalGSTUnref(p.sink)
	}
	if p.pipeline != 0 {
		portalGSTUnref(p.pipeline)
	}
	if p.fd >= 0 {
		_ = syscall.Close(p.fd)
	}
	if p.conn != 0 && portalObjectPath(p.session) {
		var failure uintptr
		reply := portalCall(p.conn, portalCString("org.freedesktop.portal.Desktop"), portalCString(p.session), portalCString("org.freedesktop.portal.Session"), portalCString("Close"), 0, 0, 0, 1000, 0, &failure)
		_ = portalFailure(failure)
		if reply != 0 {
			portalVariantUnref(reply)
		}
	}
	if p.conn != 0 {
		portalObjectUnref(p.conn)
	}
	if p.main != 0 {
		portalContextUnref(p.main)
	}
}
