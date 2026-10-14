//go:build !unix

package grants

// On platforms without flock the table is written without cross-process
// serialization. The write is still atomic (temp file + rename), so a reader
// never sees a half-written table; what is lost is the read-modify-write
// merge, meaning two golder processes writing at the same instant can drop one
// another's grant. Windows is not a supported golder platform, so this is a
// placeholder rather than a design.

import "os"

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
