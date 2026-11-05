package computeruse

import "strings"

// MatchApplications returns indexes whose application name exactly matches the
// requested name after case and whitespace normalization. It never chooses a
// target: the operator must still confirm the window and the permission scope.
// Substring or title matching is deliberately excluded because a document or a
// different application may contain the requested name.
func MatchApplications(name string, targets []Target) []int {
	want := normalizeApplicationName(name)
	if want == "" {
		return nil
	}
	var matches []int
	for i, target := range targets {
		if normalizeApplicationName(target.Application) == want {
			matches = append(matches, i)
		}
	}
	return matches
}

func normalizeApplicationName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}
