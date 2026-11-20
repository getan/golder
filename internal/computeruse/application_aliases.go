package computeruse

// Application alias groups are exact, curated names for the same application.
// They are not fuzzy matches: every alias is normalized and compared exactly, so
// a document title or a different app cannot be selected by a partial name.
var applicationAliasGroups = [][]string{
	{"豆包", "doubao"},
	{"豆包工作", "豆包work", "豆包 work", "doubaowork", "doubao work", "doubao work app"},
	{"计算器", "calculator"},
}

// expandApplicationAliases appends the known aliases of any candidate name.
func expandApplicationAliases(names []string) []string {
	out := append([]string(nil), names...)
	for _, group := range applicationAliasGroups {
		matched := false
		for _, name := range names {
			want := normalizeApplicationName(name)
			if want == "" {
				continue
			}
			for _, alias := range group {
				if normalizeApplicationName(alias) == want {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			out = append(out, group...)
		}
	}
	return out
}
