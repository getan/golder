package browserctl

import "testing"

func TestForegroundUsesHostIdentityNotBackgroundActiveFlag(t *testing.T) {
	info := Info{BrowserID: "browser", ActiveTabID: "b", Tabs: []Tab{
		{ID: "a", TargetID: "ta", URL: "https://a.test/", Active: true},
		{ID: "b", TargetID: "tb", URL: "https://b.test/"},
	}}
	tab, err := Foreground(info)
	if err != nil || tab.ID != "b" || tab.TargetID != "tb" {
		t.Fatalf("foreground = %+v %v", tab, err)
	}
	for _, active := range []string{"", "missing", "internal"} {
		info.ActiveTabID = active
		info.Tabs = append(info.Tabs, Tab{ID: "internal", TargetID: "ti", URL: "chrome://settings/"})
		if _, err := Foreground(info); err == nil {
			t.Fatalf("foreground %q fell through to a safe background tab", active)
		}
	}
}
