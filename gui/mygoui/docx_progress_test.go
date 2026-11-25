package mygoui

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/extensions"
)

func TestFormatDownloadBytes(t *testing.T) {
	cases := []struct {
		value int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{2 << 20, "2.0 MB"},
	}
	for _, tc := range cases {
		if got := formatDownloadBytes(tc.value); got != tc.want {
			t.Fatalf("formatDownloadBytes(%d) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestDocxProgressTextShowsMirrorAndBytes(t *testing.T) {
	got := docxProgressText(extensions.DocxDownloadProgress{
		Stage: "downloading", Source: "ghfast.top",
		Downloaded: 512 << 10, Total: 1 << 20,
		Attempt: 2, TotalAttempts: 5,
	})
	for _, want := range []string{"50%", "512.0 KB", "1.0 MB", "ghfast.top", "source 2/5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress text %q lacks %q", got, want)
		}
	}
	if got := docxProgressText(extensions.DocxDownloadProgress{Stage: "verifying"}); !strings.Contains(got, "SHA-256") {
		t.Fatalf("verifying text = %q", got)
	}
}
