// svglicenses collects the exact license files of the locked WASI guest's
// dependencies for inclusion alongside the packaged rendering module.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type cargoMetadata struct {
	Packages []struct {
		Name         string  `json:"name"`
		Version      string  `json:"version"`
		ManifestPath string  `json:"manifest_path"`
		License      string  `json:"license"`
		Source       *string `json:"source"`
	} `json:"packages"`
}

var licenseToken = regexp.MustCompile(`[A-Za-z0-9.-]+`)

func main() {
	out := flag.String("out", "gui/resources/renderers-LICENSE.svg.txt", "output notice path")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string) error {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, (16<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 16<<20 {
		return errors.New("invalid or oversized Cargo metadata on stdin")
	}
	var metadata cargoMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return err
	}
	sort.Slice(metadata.Packages, func(i, j int) bool {
		a, b := metadata.Packages[i], metadata.Packages[j]
		if a.Name == b.Name {
			return a.Version < b.Version
		}
		return a.Name < b.Name
	})
	var notice bytes.Buffer
	notice.WriteString("Third-party licenses for the source-built Golder static SVG WASM.\n\n")
	count := 0
	for _, pkg := range metadata.Packages {
		if pkg.Source == nil { // Golder's own MIT-licensed guest.
			continue
		}
		if !permissiveLicense(pkg.License) {
			return fmt.Errorf("unreviewed license %q for %s %s", pkg.License, pkg.Name, pkg.Version)
		}
		dir := filepath.Dir(pkg.ManifestPath)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(&notice, "\n===== %s %s (%s) =====\n", pkg.Name, pkg.Version, pkg.License)
		found := false
		for _, entry := range entries {
			upper := strings.ToUpper(entry.Name())
			if entry.IsDir() || !(strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "UNLICENSE") || strings.HasPrefix(upper, "NOTICE")) {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
				return fmt.Errorf("invalid license file for %s", pkg.Name)
			}
			text, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil || len(text) == 0 {
				return fmt.Errorf("cannot read license file for %s", pkg.Name)
			}
			fmt.Fprintf(&notice, "\n--- %s ---\n%s\n", entry.Name(), text)
			found = true
		}
		if !found {
			return fmt.Errorf("license text missing for %s %s", pkg.Name, pkg.Version)
		}
		if notice.Len() > 8<<20 {
			return errors.New("license notice exceeds size limit")
		}
		count++
	}
	if count == 0 {
		return errors.New("Cargo metadata contained no external dependencies")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	return os.WriteFile(out, notice.Bytes(), 0644)
}

func permissiveLicense(expr string) bool {
	if expr == "" {
		return false
	}
	tokens := licenseToken.FindAllString(expr, -1)
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		switch token {
		case "MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "Zlib", "0BSD", "Unlicense", "Unicode-3.0", "AND", "OR":
		default:
			return false
		}
	}
	return true
}
