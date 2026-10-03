package permissions

import "testing"

func TestParseAndString(t *testing.T) {
	cases := map[string]Mode{
		"read-only":   ReadOnly,
		"readonly":    ReadOnly,
		"RO":          ReadOnly,
		"ask":         Ask,
		"auto":        Auto,
		"full-access": FullAccess,
		"full":        FullAccess,
		"yolo":        FullAccess,
	}
	for in, want := range cases {
		got, ok := Parse(in)
		if !ok || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
		if got.String() != want.String() {
			t.Errorf("%v.String() = %q, want %q", want, got.String(), want.String())
		}
	}
	if _, ok := Parse("bogus"); ok {
		t.Error("Parse(bogus) should fail")
	}
}

func TestFromEnv(t *testing.T) {
	env := map[string]string{"GOLDER_PERMISSIONS": "read-only"}
	if m, ok := FromEnv(func(k string) string { return env[k] }); !ok || m != ReadOnly {
		t.Errorf("FromEnv = %v, %v; want read-only, true", m, ok)
	}
	legacy := map[string]string{"GOLDER_JUDGE": "off"}
	if m, ok := FromEnv(func(k string) string { return legacy[k] }); !ok || m != FullAccess {
		t.Errorf("legacy FromEnv = %v, %v; want full-access, true", m, ok)
	}
	if _, ok := FromEnv(func(string) string { return "" }); ok {
		t.Error("empty env should not resolve a mode")
	}
	if _, ok := FromEnv(nil); ok {
		t.Error("nil getenv should not resolve a mode")
	}
}

func TestState(t *testing.T) {
	st := New(Auto)
	if st.Mode() != Auto {
		t.Fatalf("initial = %v", st.Mode())
	}
	st.Set(ReadOnly)
	if st.Mode() != ReadOnly {
		t.Fatalf("after Set = %v", st.Mode())
	}
	var nilState *State
	nilState.Set(Ask) // must not panic
	if nilState.Mode() != FullAccess {
		t.Errorf("nil state should read as full-access, got %v", nilState.Mode())
	}
}

func TestLabelsLocalized(t *testing.T) {
	if got := Auto.Label("zh"); got == "" || got == Auto.Label("en") {
		t.Errorf("zh label not localized: %q", got)
	}
	if got := ReadOnly.Description("zh"); got == "" || got == ReadOnly.Description("en") {
		t.Errorf("zh description not localized: %q", got)
	}
}
