//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package session

import "os"

func lockWriterFile(*os.File) error   { return errWriterUnsupported }
func unlockWriterFile(*os.File) error { return nil }
