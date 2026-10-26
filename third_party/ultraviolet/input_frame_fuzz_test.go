package uv

import "testing"

func FuzzInputFrameChunks(f *testing.F) {
	for _, seed := range []string{
		"\x1b[<0;65;59Mhello", "\x1b]11;rgb:0000/0000/0000\x1b\\ok",
		"\x1bP1+r544e=787465726d\x1b\\", "\x1b[200~你好\x1b[<0;65;59M\x1b[201~",
		"\x1b[<\xff65;59M", "\x9bM !!", "\x1b[\x03ok", "\x1b]11;\x1b[A",
	} {
		f.Add([]byte(seed), uint8(3))
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk uint8) {
		if len(data) > 8192 {
			t.Skip()
		}
		s := newEventScanner()
		var pending []byte
		step := 1 + int(chunk)%64
		for len(data) > 0 {
			next := min(step, len(data))
			pending = append(pending, data[:next]...)
			data = data[next:]
			for _, expired := range []bool{false, true} {
				n, _ := s.scanEvents(pending, expired)
				if n < 0 || n > len(pending) {
					t.Fatalf("invalid consume count %d for %d bytes", n, len(pending))
				}
				pending = pending[n:]
			}
		}
	})
}
