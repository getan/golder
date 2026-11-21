//go:build darwin && cgo

package computeruse

/*
int cu_auth_probe(void);
int cu_auth_methods(char *, size_t);
*/
import "C"

import "unsafe"

func authProbeBits() int { return int(C.cu_auth_probe()) }

func authProbeMethods() string {
	buf := make([]byte, 8192)
	n := C.cu_auth_methods((*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	if n <= 0 {
		return ""
	}
	return string(buf[:n])
}
