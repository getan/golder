package larkmcp

import "testing"

func catalogDefinition(t *testing.T, name string) definition {
	t.Helper()
	for _, d := range definitions() {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("missing tool definition %q", name)
	return definition{}
}

func optionInt(t *testing.T, d definition, option, field string) int {
	t.Helper()
	value, ok := d.Options[option].(map[string]any)
	if !ok {
		t.Fatalf("%s.%s is not an object: %+v", d.Name, option, d.Options[option])
	}
	number, ok := value[field].(int)
	if !ok {
		t.Fatalf("%s.%s.%s is not an int: %+v", d.Name, option, field, value[field])
	}
	return number
}

func TestMessagePageSizeMatchesCLI(t *testing.T) {
	for _, name := range []string{"message_search", "chat_history", "chat_search"} {
		d := catalogDefinition(t, name)
		if got := optionInt(t, d, "page_size", "maximum"); got != 50 {
			t.Fatalf("%s page_size maximum = %d, want 50", name, got)
		}
	}
}

func TestSheetCreateIndexAllowsZero(t *testing.T) {
	d := catalogDefinition(t, "sheet_structure")
	if got := optionInt(t, d, "index", "minimum"); got != 0 {
		t.Fatalf("sheet_structure.index minimum = %d, want 0", got)
	}
	if got := optionInt(t, d, "index", "maximum"); got != 200 {
		t.Fatalf("sheet_structure.index maximum = %d, want 200", got)
	}
}

func TestBaseSchemaOmitsUnsupportedPagination(t *testing.T) {
	d := catalogDefinition(t, "base_schema")
	for _, option := range []string{"page_size", "page_token", "offset"} {
		if _, ok := d.Options[option]; ok {
			t.Fatalf("base_schema exposes unsupported option %q", option)
		}
	}
}
