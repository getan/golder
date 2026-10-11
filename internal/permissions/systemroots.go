package permissions

// The OS-level read whitelist: the paths every toolchain needs and no
// credential lives in. It lives here, beside the read-root registry, because
// two layers need the same answer — the sandbox, which admits these paths into
// its profile, and the gates, which must not ask the user about a path the
// sandbox would admit anyway. Keeping one definition means a path can never be
// "granted by the sandbox but questioned by the gate", or the reverse.

import (
	"os"
	"runtime"
)

// SystemReadRoots returns the directories a run may always read: the system
// runtime, the package-manager trees, and the temp roots a shell needs. It is
// deliberately short — $HOME is NOT here, because the point of the whitelist is
// that credential material inside $HOME is excluded by construction rather than
// by an ever-growing blacklist. A user whose toolchain lives under $HOME
// (conda, pyenv, nvm) adds it via [sandbox] readable or GOLDER_SANDBOX_READABLE.
func SystemReadRoots() []string {
	roots := []string{
		// System runtime and libraries: exec, dyld, homebrew.
		"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/etc",
	}
	if runtime.GOOS == "darwin" {
		roots = append(roots,
			"/System", "/Library", "/Applications",
			"/private/var/db", "/private/etc", "/private/tmp", "/private/var/tmp",
		)
	} else {
		roots = append(roots, "/var/lib", "/var/cache")
	}
	// Device nodes, and the machine's temp directory wherever TMPDIR puts it
	// (/var/folders/… on macOS, /tmp on Linux). The sandbox admits its own temp
	// root into the read whitelist, so the gate must exempt the same place: a
	// path the sandbox would serve is not a consent question. Note that a
	// literal "/tmp/x" is NOT covered by "/private/tmp" — this check compares
	// cleaned paths rather than resolving symlinks the way the kernel does.
	roots = append(roots, "/dev", "/proc")
	if tmp := os.TempDir(); tmp != "" {
		roots = append(roots, tmp)
	}
	return roots
}
