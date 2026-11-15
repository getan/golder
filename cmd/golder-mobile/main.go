//go:build android && cgo

// Command golder-mobile is the Android shared library. It exposes a small C
// ABI for diagnostics and JNI methods for the Kotlin host. The service itself
// lives in internal/mobilebridge so it can be tested without a JVM.
package main

/*
#include <jni.h>
#include <stdlib.h>

static jsize golder_str_len(JNIEnv *env, jstring s) {
	return (*env)->GetStringLength(env, s);
}

static const jchar* golder_str_chars(JNIEnv *env, jstring s) {
	return (*env)->GetStringChars(env, s, NULL);
}

static void golder_release_chars(JNIEnv *env, jstring s, const jchar* p) {
	(*env)->ReleaseStringChars(env, s, p);
}

static jstring golder_new_utf16(JNIEnv *env, const jchar* p, jsize n) {
	return (*env)->NewString(env, p, n);
}
*/
import "C"

import (
	"runtime"
	"unicode/utf16"
	"unsafe"

	"github.com/getan/golder/internal/mobilebridge"
)

var service = mobilebridge.NewService(mobilebridge.NewAndroidSession)

func main() {}

func goString(env *C.JNIEnv, s C.jstring) string {
	if s == 0 {
		return ""
	}
	n := int(C.golder_str_len(env, s))
	if n == 0 {
		return ""
	}
	p := C.golder_str_chars(env, s)
	if p == nil {
		return ""
	}
	defer C.golder_release_chars(env, s, p)
	units := unsafe.Slice((*uint16)(unsafe.Pointer(p)), n)
	return string(utf16.Decode(units))
}

func javaString(env *C.JNIEnv, s string) C.jstring {
	units := utf16.Encode([]rune(s))
	if len(units) == 0 {
		return C.golder_new_utf16(env, nil, 0)
	}
	out := C.golder_new_utf16(env, (*C.jchar)(unsafe.Pointer(&units[0])), C.jsize(len(units)))
	runtime.KeepAlive(units)
	return out
}

func failOpen() C.jint { return 0 }

// The C ABI is used by the device ABI smoke test and by a host that prefers
// explicit function pointers over JNI symbol lookup.

//export golder_mobile_version
func golder_mobile_version() *C.char {
	return C.CString(mobilebridge.Version)
}

//export golder_mobile_catalog
func golder_mobile_catalog() *C.char {
	raw, err := service.CatalogJSON()
	if err != nil {
		return nil
	}
	return C.CString(raw)
}

//export golder_mobile_free
func golder_mobile_free(p *C.char) {
	C.free(unsafe.Pointer(p))
}

//export golder_mobile_last_error
func golder_mobile_last_error() *C.char {
	return C.CString(service.LastError())
}

//export golder_mobile_open
func golder_mobile_open(config *C.char) C.int {
	if config == nil {
		return 0
	}
	handle, err := service.Open(C.GoString(config))
	if err != nil {
		return 0
	}
	return C.int(handle)
}

//export golder_mobile_start
func golder_mobile_start(handle C.int, prompt *C.char) C.int {
	if prompt == nil {
		return -1
	}
	if err := service.Start(int(handle), C.GoString(prompt)); err != nil {
		return -1
	}
	return 0
}

//export golder_mobile_poll
func golder_mobile_poll(handle C.int, max C.int) *C.char {
	raw, err := service.PollJSON(int(handle), int(max))
	if err != nil {
		return nil
	}
	return C.CString(raw)
}

//export golder_mobile_snapshot
func golder_mobile_snapshot(handle C.int) *C.char {
	raw, err := service.SnapshotJSON(int(handle))
	if err != nil {
		return nil
	}
	return C.CString(raw)
}

//export golder_mobile_cancel
func golder_mobile_cancel(handle C.int) {
	_ = service.Cancel(int(handle))
}

//export golder_mobile_close
func golder_mobile_close(handle C.int) {
	_ = service.Close(int(handle))
}

// JNI methods for com.getan.golder.mobile.GolderNative. The class may expose
// them as external instance methods; the second argument is unused and has the
// same ABI width as jclass.

//export Java_com_getan_golder_mobile_GolderNative_version
func Java_com_getan_golder_mobile_GolderNative_version(env *C.JNIEnv, clazz C.jclass) C.jstring {
	return javaString(env, mobilebridge.Version)
}

//export Java_com_getan_golder_mobile_GolderNative_catalog
func Java_com_getan_golder_mobile_GolderNative_catalog(env *C.JNIEnv, clazz C.jclass) C.jstring {
	raw, err := service.CatalogJSON()
	if err != nil {
		return 0
	}
	return javaString(env, raw)
}

//export Java_com_getan_golder_mobile_GolderNative_lastError
func Java_com_getan_golder_mobile_GolderNative_lastError(env *C.JNIEnv, clazz C.jclass) C.jstring {
	return javaString(env, service.LastError())
}

//export Java_com_getan_golder_mobile_GolderNative_open
func Java_com_getan_golder_mobile_GolderNative_open(env *C.JNIEnv, clazz C.jclass, config C.jstring) C.jint {
	handle, err := service.Open(goString(env, config))
	if err != nil {
		return failOpen()
	}
	return C.jint(handle)
}

//export Java_com_getan_golder_mobile_GolderNative_start
func Java_com_getan_golder_mobile_GolderNative_start(env *C.JNIEnv, clazz C.jclass, handle C.jint, prompt C.jstring) C.jint {
	if err := service.Start(int(handle), goString(env, prompt)); err != nil {
		return -1
	}
	return 0
}

//export Java_com_getan_golder_mobile_GolderNative_poll
func Java_com_getan_golder_mobile_GolderNative_poll(env *C.JNIEnv, clazz C.jclass, handle C.jint, max C.jint) C.jstring {
	raw, err := service.PollJSON(int(handle), int(max))
	if err != nil {
		return 0
	}
	return javaString(env, raw)
}

//export Java_com_getan_golder_mobile_GolderNative_snapshot
func Java_com_getan_golder_mobile_GolderNative_snapshot(env *C.JNIEnv, clazz C.jclass, handle C.jint) C.jstring {
	raw, err := service.SnapshotJSON(int(handle))
	if err != nil {
		return 0
	}
	return javaString(env, raw)
}

//export Java_com_getan_golder_mobile_GolderNative_cancel
func Java_com_getan_golder_mobile_GolderNative_cancel(env *C.JNIEnv, clazz C.jclass, handle C.jint) {
	_ = service.Cancel(int(handle))
}

//export Java_com_getan_golder_mobile_GolderNative_close
func Java_com_getan_golder_mobile_GolderNative_close(env *C.JNIEnv, clazz C.jclass, handle C.jint) {
	_ = service.Close(int(handle))
}
