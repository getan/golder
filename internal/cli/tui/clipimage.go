package tui

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// This file implements the platform-specific clipboard-image read behind Ctrl+V
// (and Cmd+V) image paste, mirroring Claude Code: when the system clipboard holds
// a raster image the model saves it to a temp PNG and drops an "[Image #N]"
// placeholder into the composer (see model.handleImagePaste), expanded at submit
// into an "@image:<path>" reference that BuildUserContent attaches as multimodal
// content. Text clipboards fall through to the normal OSC52 read.

// clipboardImageMsg is the reply to a clipboard-image read attempt. When ok is
// true, path is the temp PNG the decoded image was written to; when ok is false
// the clipboard held no image (or no reader tool was available) and the caller
// falls back to a plain text read.
type clipboardImageMsg struct {
	path string
	ok   bool
}

// readClipboardImage is a tea.Cmd that tries to pull a raster image out of the
// system clipboard and save it as a PNG under the OS temp dir. It shells out to
// the platform's clipboard tool (macOS: osascript; Linux: wl-paste or xclip). Any
// failure — no image on the clipboard, a missing tool — yields ok=false so the
// caller can fall back to a normal text paste rather than surfacing an error.
func readClipboardImage() tea.Msg {
	path, ok := saveClipboardImage()
	return clipboardImageMsg{path: path, ok: ok}
}

// saveClipboardImage writes the clipboard image to a fresh temp PNG and returns
// its path, or ok=false when the clipboard holds no image. The temp file is
// removed on any failure so a stray empty file is never left behind.
func saveClipboardImage() (string, bool) {
	f, err := os.CreateTemp("", "golder-clip-*.png")
	if err != nil {
		return "", false
	}
	path := f.Name()
	f.Close()

	var okRead bool
	switch runtime.GOOS {
	case "darwin":
		okRead = saveClipboardImageDarwin(path)
	case "linux":
		okRead = saveClipboardImageLinux(path)
	}
	if !okRead {
		os.Remove(path)
		return "", false
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		os.Remove(path)
		return "", false
	}
	return path, true
}

// darwinClipboardScript asks the pasteboard for its contents as PNG data and
// writes the raw bytes to the path passed as the first argv item, returning "ok"
// on success or "noimage" when the clipboard cannot be coerced to an image.
const darwinClipboardScript = `on run argv
	set outPath to item 1 of argv
	try
		set pngData to (the clipboard as «class PNGf»)
	on error
		return "noimage"
	end try
	set fh to open for access (POSIX file outPath) with write permission
	set eof fh to 0
	write pngData to fh
	close access fh
	return "ok"
end run`

// saveClipboardImageDarwin uses osascript (always present on macOS) to coerce the
// pasteboard to PNG and write it to path.
func saveClipboardImageDarwin(path string) bool {
	out, err := exec.Command("osascript", "-e", darwinClipboardScript, path).Output()
	return err == nil && strings.TrimSpace(string(out)) == "ok"
}

// saveClipboardImageLinux reads an image/png off the clipboard via wl-paste
// (Wayland) or xclip (X11), preferring whichever is installed. It first checks the
// advertised MIME types so a text-only clipboard is not mistaken for an image.
func saveClipboardImageLinux(path string) bool {
	if _, err := exec.LookPath("wl-paste"); err == nil {
		types, _ := exec.Command("wl-paste", "--list-types").Output()
		if strings.Contains(string(types), "image/png") {
			if writeCmdOutput(path, exec.Command("wl-paste", "--type", "image/png")) {
				return true
			}
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		targets, _ := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
		if strings.Contains(string(targets), "image/png") {
			if writeCmdOutput(path, exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o")) {
				return true
			}
		}
	}
	return false
}

// writeCmdOutput runs cmd and writes its stdout to path, reporting whether it
// produced any bytes.
func writeCmdOutput(path string, cmd *exec.Cmd) bool {
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return false
	}
	return os.WriteFile(path, out, 0o600) == nil
}
