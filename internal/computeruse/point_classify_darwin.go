//go:build darwin && cgo

package computeruse

/*
#include <stdlib.h>
int cu_point_classify_test(int found, int metadata, int overlap, int hit, const char **out);
*/
import "C"

// Bridges the pure C decision table to tests without real windows.
func macPointClassifyLabel(found, metadata, overlap, hit int, label *string) bool {
	var out *C.char
	if int(C.cu_point_classify_test(C.int(found), C.int(metadata), C.int(overlap), C.int(hit), &out)) != 0 {
		return false
	}
	*label = C.GoString(out)
	return true
}
