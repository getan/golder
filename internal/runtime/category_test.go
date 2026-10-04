package runtime

import "testing"

func TestCategoryOf(t *testing.T) {
	cases := []struct {
		name string
		cmd  SlashCommand
		want string
	}{
		{"builtin defaults to general", SlashCommand{Name: "help", Source: SourceBuiltin}, CategoryGeneral},
		{"builtin keeps its category", SlashCommand{Name: "model", Source: SourceBuiltin, Category: CategoryModel}, CategoryModel},
		{"user command is an extension", SlashCommand{Name: "review", Source: SourceUser}, CategoryExtensions},
		{"skill is an extension regardless of category", SlashCommand{Name: "prd", Source: SourceSkill, Category: CategoryModel}, CategoryExtensions},
		{"plugin is an extension", SlashCommand{Name: "deploy", Source: SourcePlugin}, CategoryExtensions},
	}
	for _, c := range cases {
		if got := CategoryOf(c.cmd); got != c.want {
			t.Errorf("%s: CategoryOf(%q) = %q, want %q", c.name, c.cmd.Name, got, c.want)
		}
	}
}
