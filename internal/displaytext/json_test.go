package displaytext

import (
	"strings"
	"testing"
)

func TestPrettyJSONIsDisplayOnlyAndPreservesValues(t *testing.T) {
	raw := `{"n":9007199254740993,"text":"中文{\\\"}","items":[true,null]}`
	got := PrettyJSON(raw)
	if !strings.Contains(got, "\n  \"n\": 9007199254740993") || !strings.Contains(got, `"items": [`) {
		t.Fatalf("JSON not indented without numeric loss: %q", got)
	}
	for _, raw := range []string{"ordinary output", "{invalid}", "prefix {\"ok\":true}", strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)} {
		if PrettyJSON(raw) != raw {
			t.Fatal("non-JSON or excessive nesting changed")
		}
	}
}

func TestPrettyJSONReadPreservesLineNumbersAndReadNotices(t *testing.T) {
	raw := "     1\t{\"name\":\"golder\",\"items\":[1,2]}\n"
	got := PrettyJSONRead(raw)
	for _, want := range []string{
		"     1\t{",
		"     2\t  \"name\": \"golder\",",
		"     3\t  \"items\": [",
		"     7\t}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("numbered JSON is missing %q: %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("trailing newline was dropped")
	}
	notice := "\n... (output truncated after line 1 (1-line limit); resume with offset=2 and a smaller limit)\n"
	got = PrettyJSONRead("     1\t{\"a\":1}\n" + notice)
	if !strings.Contains(got, "     1\t{") || !strings.Contains(got, notice) {
		t.Fatalf("read notice or line number was not preserved: %q", got)
	}
	for _, unchanged := range []string{
		"ordinary text\n",
		"1\t{bad json}\n",
		"not numbered {\"a\":1}\n",
	} {
		if PrettyJSONRead(unchanged) != unchanged {
			t.Fatalf("non-JSON read output changed: %q", unchanged)
		}
	}
}
