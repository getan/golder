//go:build !unix

package computeruse

import "os"

func queryPaneGraphics(_, _ *os.File) bool { return false }
