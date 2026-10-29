package uv

import (
	"strings"
	"testing"
)

// A CSI frame that never terminates must not swallow the bytes typed after it.
// CSI parameters and intermediates are ASCII (0x20-0x3f / 0x20-0x2f) and the
// final byte is 0x40-0x7e, so a UTF-8 lead byte can only be ordinary input.
// Before this case was handled, a terminal that split "ESC [" from the rest of
// its report left every following CJK character buffered until an ASCII final
// byte arrived: IME text simply never reached the composer.
func TestInputEPrefixDoesNotSwallowCJK(t *testing.T) {
	for name, seq := range map[string]string{
		"csi then cjk":   "\x1b[中文",
		"csi then emoji": "\x1b[你好🌍",
		"ss3 then cjk":   "\x1bO中文",
	} {
		t.Run(name, func(t *testing.T) {
			want := "中文"
			if strings.Contains(seq, "你好") {
				want = "你好🌍"
			}
			for i := 1; i < len(seq); i++ {
				var got strings.Builder
				for _, e := range splitScan(seq[:i], seq[i:]) {
					if k, ok := e.(KeyPressEvent); ok {
						got.WriteString(k.Text)
					}
				}
				if !strings.Contains(got.String(), want) {
					t.Fatalf("split %d/%d: typed %q, got %q", i, len(seq), want, got.String())
				}
			}
		})
	}
}

// The same guarantee after an oversized frame switched the scanner into
// recovery mode: CJK must end the discard, not be discarded with it.
func TestDiscardRecoversAtCJK(t *testing.T) {
	s := newEventScanner()
	// A still-open CSI frame larger than maxInputSequence puts the scanner in
	// discard mode. Keep it ASCII so the fixture reaches that state: a UTF-8
	// lead byte now ends the frame instead of joining it.
	huge := "\x1b[" + strings.Repeat("1;", maxInputSequence)
	if n, _ := s.scanEvents([]byte(huge), false); n != len(huge) {
		t.Fatalf("oversized frame consumed %d of %d bytes", n, len(huge))
	}
	if s.discard == inputPlain {
		t.Fatal("fixture did not enter discard mode")
	}
	_, events := s.scanEvents([]byte("中文"), false)
	var got strings.Builder
	for _, e := range events {
		if k, ok := e.(KeyPressEvent); ok {
			got.WriteString(k.Text)
		}
	}
	if got.String() != "中文" {
		t.Fatalf("after discarding an oversized frame, typed %q, got %q", "中文", got.String())
	}
}
