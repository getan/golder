// fetchmermaid acquires a pinned upstream MIT-licensed Mermaid WASM at build
// time. The packaged application never downloads rendering code at runtime.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/getan/golder/gui/mermaidwasm"
)

const moduleURL = "https://raw.githubusercontent.com/akashacms/mermaid-wasm-renderer/cf6338f0e905d02620296ad77f75ec9d59ecd7b8/pkg/mermaid_wasm_renderer_bg.wasm"

func main() {
	out := flag.String("out", "gui/resources/renderers/mermaid.wasm", "output WASM path")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, moduleURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Hostname() != "raw.githubusercontent.com" || len(via) > 3 {
			return errors.New("unexpected module redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download Mermaid WASM: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download Mermaid WASM: HTTP %d", resp.StatusCode)
	}
	wasm, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(wasm) > 4<<20 {
		return errors.New("invalid or oversized Mermaid module")
	}
	if err = verify(wasm, mermaidwasm.ModuleSHA256); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(out), ".mermaid-wasm-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(wasm); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), out)
}

func verify(wasm []byte, wasmHash string) error {
	if len(wasm) == 0 || len(wasm) > 4<<20 {
		return errors.New("invalid Mermaid module size")
	}
	sum := sha256.Sum256(wasm)
	if hex.EncodeToString(sum[:]) != wasmHash {
		return errors.New("Mermaid WASM checksum mismatch")
	}
	return nil
}
