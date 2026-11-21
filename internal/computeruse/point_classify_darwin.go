//go:build darwin && cgo

package computeruse

/*
#include <stdlib.h>
int cu_point_classify_test(int found, int metadata, int overlap, int hit, const char **out);
int cu_ax_block_reason_test(const char *role, const char *subrole, int response, const char **out);
*/
import "C"

import "unsafe"

// Bridges the pure C decision table to tests without real windows.
func macPointClassifyLabel(found, metadata, overlap, hit int, label *string) bool {
	var out *C.char
	if int(C.cu_point_classify_test(C.int(found), C.int(metadata), C.int(overlap), C.int(hit), &out)) != 0 {
		return false
	}
	*label = C.GoString(out)
	return true
}

func macAXBlockReason(role, subrole string, response int) (string, bool) {
	crole := C.CString(role)
	defer C.free(unsafe.Pointer(crole))
	var csubrole *C.char
	if subrole != "" {
		csubrole = C.CString(subrole)
		defer C.free(unsafe.Pointer(csubrole))
	}
	var out *C.char
	if int(C.cu_ax_block_reason_test(crole, csubrole, C.int(response), &out)) != 0 {
		return "", false
	}
	return C.GoString(out), true
}
