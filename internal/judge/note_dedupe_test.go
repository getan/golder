package judge

import (
	"strings"
	"testing"
)

func TestFormatNoteShowsGrantedCapabilitiesOnlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang string
		want string
	}{
		{"zh", "zh", "已授权：读取 /tmp/outside"},
		{"en", "en", "granted: read /tmp/outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := Note{
				Tool: "bash", Mode: "auto", Level: Allow, Source: "llm",
				Kind: NoteEscaped, Lang: tc.lang,
				Granted:   []Need{{Cap: CapReadOutside, Path: "/tmp/outside"}},
				Rationale: tc.want,
			}
			got := FormatNote(note)
			if strings.Count(got, tc.want) != 1 {
				t.Fatalf("granted capability repeated or missing:\n%s", got)
			}
			if strings.Contains(got, "): "+tc.want) {
				t.Fatalf("rationale repeats the capability suffix:\n%s", got)
			}
		})
	}
}

func TestFormatNoteKeepsAnIndependentRationale(t *testing.T) {
	note := Note{
		Tool: "bash", Mode: "auto", Level: Allow, Source: "llm",
		Kind: NoteEscaped, Lang: "zh",
		Granted:   []Need{{Cap: CapReadOutside, Path: "/tmp/outside"}},
		Rationale: "用户要求读取这个临时目录",
	}
	got := FormatNote(note)
	if !strings.Contains(got, "（用户要求读取这个临时目录）") && !strings.HasSuffix(got, ": 用户要求读取这个临时目录") {
		t.Fatalf("independent rationale was dropped: %s", got)
	}
	if strings.Count(got, "已授权") != 1 {
		t.Fatalf("capability suffix changed: %s", got)
	}
}
