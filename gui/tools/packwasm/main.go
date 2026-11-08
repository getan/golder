// packwasm compresses a build-produced WASM resource without changing the
// bytes the application verifies and executes after decompression.
package main

import (
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxWASM = 4 << 20

func main() {
	in := flag.String("in", "", "input WASM")
	out := flag.String("out", "", "output gzip")
	flag.Parse()
	if err := pack(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pack(in, out string) error {
	if in == "" || out == "" {
		return errors.New("input and output paths are required")
	}
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxWASM+1))
	if err != nil || len(data) < 8 || len(data) > maxWASM || string(data[:4]) != "\x00asm" {
		return errors.New("invalid or oversized WASM")
	}
	if err = os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".wasm-gzip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw, err := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if err != nil {
		tmp.Close()
		return err
	}
	if _, err = zw.Write(data); err != nil {
		zw.Close()
		tmp.Close()
		return err
	}
	if err = zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}
