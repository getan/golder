package extensions

import (
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func validateLocalCapabilities(c SourceConfig) error {
	if c.Transport == "http" && (c.StateDir || len(c.Setup)+len(c.SetupVariants)+len(c.Executables)+len(c.SetupOrigins) > 0) {
		return errors.New("state_dir, setup and executables require a local process")
	}
	for _, p := range append(append([]string{}, c.Executables...), c.Skills) {
		if p != "" && !filepath.IsAbs(expandPath(p)) {
			return errors.New("executables and skills must use absolute paths")
		}
	}
	if len(c.Executables) > 32 || len(c.Setup) > 64 || len(c.SetupOrigins) > 32 {
		return errors.New("too many local capability declarations")
	}
	if len(c.Setup) > 0 && (!c.StateDir || !filepath.IsAbs(expandPath(c.Setup[0]))) {
		return errors.New("setup requires state_dir and an absolute executable")
	}
	if len(c.SetupVariants) > 16 {
		return errors.New("too many setup variants")
	}
	for name, argv := range c.SetupVariants {
		if !idPattern.MatchString(name) || len(argv) == 0 || len(argv) > 64 || !c.StateDir || !filepath.IsAbs(expandPath(argv[0])) {
			return errors.New("setup variants require a valid name and fixed absolute argv with state_dir")
		}
	}
	for _, origin := range c.SetupOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
			return errors.New("setup_origins must be exact HTTPS origins")
		}
	}
	return nil
}

// Aggregate entry points can otherwise bypass a deny on a narrower tool. Until
// a source offers a host-verifiable command/capability mapping, fail closed as
// soon as any tool in its catalog is filtered out (also by model policy).
func (m *Manager) toolAllowed(s *source, name string) bool {
	if !allowed(s.cfg, name) || m.opts.Allow != nil && !m.opts.Allow(ModelName(s.ref, name)) {
		return false
	}
	aggregate := false
	for _, pattern := range s.cfg.AggregateTools {
		if ok, _ := filepath.Match(pattern, name); ok {
			aggregate = true
		}
	}
	if !aggregate {
		return true
	}
	for _, spec := range s.catalog.Tools {
		if !allowed(s.cfg, spec.Name) || m.opts.Allow != nil && !m.opts.Allow(ModelName(s.ref, spec.Name)) {
			return false
		}
	}
	return true
}

// Hash the instruction package too: a changed Skill cannot retain an old source
// approval. Symlinks are not package files, including links to sibling secrets.
func hashSkills(w io.Writer, dir string) error {
	dir = expandPath(dir)
	n := 0
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("skill package contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("skill package contains a non-regular file")
		}
		n++
		if n > 128 {
			return errors.New("skill package exceeds 128 files")
		}
		b, err := readLimited(path, 256<<10)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		io.WriteString(w, strings.ReplaceAll(rel, "\\", "/")+"\x00")
		_, err = w.Write(b)
		return err
	})
}
