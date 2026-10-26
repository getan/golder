package agenttool

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pngWithDimensions builds a PNG signature+IHDR+IEND. DecodeConfig only needs
// the header, so this can describe a huge frame without allocating its pixels.
func pngWithDimensions(w, h uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	writeTestPNGChunk(&b, "IHDR", ihdr)
	writeTestPNGChunk(&b, "IEND", nil)
	return b.Bytes()
}

func writeTestPNGChunk(b *bytes.Buffer, kind string, data []byte) {
	_ = binary.Write(b, binary.BigEndian, uint32(len(data)))
	b.WriteString(kind)
	b.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(kind))
	_, _ = crc.Write(data)
	_ = binary.Write(b, binary.BigEndian, crc.Sum32())
}

func TestViewImageRejectsOversizedDecodedDimensions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(path, pngWithDimensions(5000, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runViewImage(t, &ViewImageTool{Root: dir}, map[string]any{"path": "huge.png"})
	if !res.IsError || !strings.Contains(resultText(res), "decode limit") {
		t.Fatalf("result = %q, want the pixel-budget refusal", resultText(res))
	}
}

func TestViewImageRunsOneTaskAtATime(t *testing.T) {
	viewImageSlot <- struct{}{}
	defer func() { <-viewImageSlot }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	args, _ := json.Marshal(map[string]any{"path": "x.png"})
	res, err := (&ViewImageTool{Root: t.TempDir()}).Execute(ctx, "1", args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(resultText(res), "cancelled while waiting") {
		t.Fatalf("result = %q, want a cancellable wait for the image slot", resultText(res))
	}
}
