//go:build windows

package agenttool

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

var (
	kernel32     = windows.NewLazySystemDLL("kernel32.dll")
	procGetOEMCP = kernel32.NewProc("GetOEMCP")
	procGetACP   = kernel32.NewProc("GetACP")
)

// normalizeShellOutput converts captured native output to valid UTF-8. Windows
// console programs commonly write the OEM or ANSI code page even when stdout is
// a pipe; PowerShell is asked to emit UTF-8 separately, but cmd and native tools
// are not, so the bytes are decoded here. Bytes that no supported code page can
// explain are escaped as \xNN instead of being replaced by U+FFFD: the old
// behavior destroyed exactly the diagnostic text a failure needed.
func normalizeShellOutput(s string) string {
	if s == "" || utf8.ValidString(s) {
		return s
	}
	for _, cp := range []uint32{oemCodePage(), ansiCodePage()} {
		if cp == 0 || cp == 65001 {
			continue
		}
		enc := encodingForCodePage(cp)
		if enc == nil {
			continue
		}
		decoded, err := enc.NewDecoder().String(s)
		if err == nil && utf8.ValidString(decoded) && !strings.ContainsRune(decoded, utf8.RuneError) {
			return decoded
		}
	}
	return escapeInvalidUTF8(s)
}

func oemCodePage() uint32 {
	cp, _, _ := procGetOEMCP.Call()
	return uint32(cp)
}

func ansiCodePage() uint32 {
	cp, _, _ := procGetACP.Call()
	return uint32(cp)
}

func encodingForCodePage(cp uint32) encoding.Encoding {
	switch cp {
	case 932:
		return japanese.ShiftJIS
	case 936:
		return simplifiedchinese.GBK
	case 949:
		return korean.EUCKR
	case 950:
		return traditionalchinese.Big5
	case 437:
		return charmap.CodePage437
	case 850:
		return charmap.CodePage850
	case 852:
		return charmap.CodePage852
	case 855:
		return charmap.CodePage855
	case 858:
		return charmap.CodePage858
	case 860:
		return charmap.CodePage860
	case 862:
		return charmap.CodePage862
	case 863:
		return charmap.CodePage863
	case 865:
		return charmap.CodePage865
	case 866:
		return charmap.CodePage866
	case 874:
		return charmap.Windows874
	case 1250:
		return charmap.Windows1250
	case 1251:
		return charmap.Windows1251
	case 1252:
		return charmap.Windows1252
	case 1253:
		return charmap.Windows1253
	case 1254:
		return charmap.Windows1254
	case 1255:
		return charmap.Windows1255
	case 1256:
		return charmap.Windows1256
	case 1257:
		return charmap.Windows1257
	case 1258:
		return charmap.Windows1258
	}
	return nil
}

// escapeInvalidUTF8 preserves every byte. It is the last resort when the
// encoding cannot be identified, so a diagnostic is never silently turned
// into U+FFFD.
func escapeInvalidUTF8(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, `\x%02x`, s[0])
			s = s[1:]
			continue
		}
		b.WriteString(s[:size])
		s = s[size:]
	}
	return b.String() + "\n[output contained bytes that were not valid UTF-8 or a known Windows code page; they are shown as \\xNN]"
}
