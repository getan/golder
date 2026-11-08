// preparemermaid reconstructs the pinned upstream manifest tree for a
// build-time license audit. It does not compile or execute upstream code.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/gui/mermaidwasm"
)

const (
	commit        = "cf6338f0e905d02620296ad77f75ec9d59ecd7b8"
	sourceURL     = "https://codeload.github.com/akashacms/mermaid-wasm-renderer/tar.gz/" + commit
	sourceHash    = "97e3cf59afaa69529a6ba33d4d40ac58d05acf992862e58d5376193dc9cef6db"
	crateURL      = "https://static.crates.io/crates/mermaid-rs-renderer/mermaid-rs-renderer-0.3.1.crate"
	crateHash     = "6cb5b469222ce6b9896edf0a969cf68464f5a028ef394fd6643f00bd943df117"
	moduleMember  = "pkg/mermaid_wasm_renderer_bg.wasm"
	maxArchive    = 8 << 20
	maxExtraction = 24 << 20
)

func main() {
	out := flag.String("out", "", "new directory for the upstream manifest tree")
	flag.Parse()
	if err := prepare(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare(out string) error {
	if out == "" {
		return errors.New("output directory is required")
	}
	source, err := download(sourceURL, sourceHash)
	if err != nil {
		return err
	}
	crate, err := download(crateURL, crateHash)
	if err != nil {
		return err
	}
	if err = os.Mkdir(out, 0755); err != nil {
		return err
	}
	selected := map[string]bool{"Cargo.toml": true, "Cargo.lock": true, "src/lib.rs": true, moduleMember: true}
	seen, err := extract(source, "mermaid-wasm-renderer-"+commit+"/", out, selected, true)
	if err != nil {
		return err
	}
	for name := range selected {
		if !seen[name] {
			return fmt.Errorf("upstream source missing %s", name)
		}
	}
	vendor := filepath.Join(out, "vendor", "mermaid-rs-renderer")
	if err = os.MkdirAll(vendor, 0755); err != nil {
		return err
	}
	_, err = extract(crate, "mermaid-rs-renderer-0.3.1/", vendor, nil, false)
	return err
}

func download(url, want string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" || next.URL.Hostname() != req.URL.Hostname() || len(via) > 3 {
			return errors.New("unexpected source redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source download: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil || len(data) == 0 || len(data) > maxArchive {
		return nil, errors.New("invalid or oversized source archive")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return nil, errors.New("source archive checksum mismatch")
	}
	return data, nil
}

func extract(data []byte, prefix, dest string, selected map[string]bool, verifyModule bool) (map[string]bool, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := make(map[string]bool)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(h.Name, prefix) {
			continue
		}
		name := strings.TrimPrefix(h.Name, prefix)
		if name == "" || h.Typeflag == tar.TypeDir {
			continue
		}
		if path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:") {
			return nil, errors.New("invalid source archive path")
		}
		if selected != nil && !selected[name] {
			continue
		}
		if h.Typeflag != tar.TypeReg || seen[name] || h.Size < 0 || h.Size > maxArchive {
			return nil, errors.New("invalid source archive entry")
		}
		total += h.Size
		if total > maxExtraction {
			return nil, errors.New("source archive exceeds limit")
		}
		contents, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil || int64(len(contents)) != h.Size {
			return nil, errors.New("invalid source archive contents")
		}
		seen[name] = true
		if verifyModule && name == moduleMember {
			sum := sha256.Sum256(contents)
			if hex.EncodeToString(sum[:]) != mermaidwasm.ModuleSHA256 {
				return nil, errors.New("source tree does not contain the pinned module")
			}
			continue
		}
		filename := filepath.Join(dest, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
			return nil, err
		}
		if err = os.WriteFile(filename, contents, 0644); err != nil {
			return nil, err
		}
	}
	return seen, nil
}
