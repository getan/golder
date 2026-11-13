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
