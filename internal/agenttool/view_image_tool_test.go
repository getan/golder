package agenttool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// writeTestPNG writes a tiny valid PNG under dir and returns its path.
func writeTestPNG(t *testing.T, dir string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	path := filepath.Join(dir, "shot.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return path
}

func runViewImage(t *testing.T, tool *ViewImageTool, args map[string]any) agentcore.AgentToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, execErr := tool.Execute(context.Background(), "call-1", raw, nil)
	if execErr != nil {
		t.Fatalf("view_image: %v", execErr)
	}
	return res
}

func TestViewImageReturnsImageBlock(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, dir)
	tool := &ViewImageTool{Root: dir}

	res := runViewImage(t, tool, map[string]any{"path": "shot.png"})
	if len(res.Content) != 2 {
		t.Fatalf("content has %d blocks, want text + image", len(res.Content))
	}
	if text, ok := res.Content[0].(agentcore.TextContent); !ok || !strings.Contains(text.Text, "shot.png") {
		t.Fatalf("first block = %#v, want a text summary", res.Content[0])
	}
	img, ok := res.Content[1].(agentcore.ImageContent)
	if !ok {
		t.Fatalf("second block = %T, want ImageContent", res.Content[1])
	}
	if img.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", img.MimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil || len(decoded) == 0 {
		t.Fatalf("image data is not valid base64 (%v)", err)
	}
	details, _ := res.Details.(map[string]any)
	if details["mime"] != "image/png" || details["detail"] != "high" {
		t.Errorf("details = %#v", res.Details)
	}
}

func TestViewImageDetailArg(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, dir)
	tool := &ViewImageTool{Root: dir}
	res := runViewImage(t, tool, map[string]any{"path": "shot.png", "detail": "original"})
	if details, _ := res.Details.(map[string]any); details["detail"] != "original" {
		t.Errorf("details = %#v, want detail original", res.Details)
	}
	bad := runViewImage(t, tool, map[string]any{"path": "shot.png", "detail": "low"})
	if !strings.Contains(resultText(bad), "detail") {
		t.Errorf("invalid detail = %q, want a validation error", resultText(bad))
	}
}

func TestViewImageRejectsEscapeAndMissing(t *testing.T) {
	dir := t.TempDir()
	tool := &ViewImageTool{Root: dir}
	escape := runViewImage(t, tool, map[string]any{"path": "../outside.png"})
	if !strings.Contains(resultText(escape), "outside") && !strings.Contains(resultText(escape), "escape") {
		t.Errorf("escape result = %q, want a boundary rejection", resultText(escape))
	}
	missing := runViewImage(t, tool, map[string]any{"path": "nope.png"})
	if !strings.Contains(resultText(missing), "does not exist") {
		t.Errorf("missing result = %q, want a not-found error", resultText(missing))
	}
}

func TestViewImageRejectsNonImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tool := &ViewImageTool{Root: dir}
	res := runViewImage(t, tool, map[string]any{"path": "notes.txt"})
	if !strings.Contains(resultText(res), "not a recognized image") {
		t.Errorf("result = %q, want a not-an-image error", resultText(res))
	}
}

func TestViewImageSizeCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sparse truncate differs on windows")
	}
	dir := t.TempDir()
	path := writeTestPNG(t, dir)
	// Grow the file past the cap without writing the bytes (sparse).
	if err := os.Truncate(path, viewImageMaxBytes+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	tool := &ViewImageTool{Root: dir}
	res := runViewImage(t, tool, map[string]any{"path": "shot.png"})
	if !strings.Contains(resultText(res), "larger than") {
		t.Errorf("result = %q, want an oversize rejection", resultText(res))
	}
}
