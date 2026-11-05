package computeruse

import (
	"slices"
	"testing"
)

func TestMatchApplicationsNeverGuessFromPartialNamesOrTitles(t *testing.T) {
	targets := []Target{
		{ID: "one", Application: "Calculator", Window: "Main"},
		{ID: "two", Application: "Calculator", Window: "History"},
		{ID: "three", Application: "Other Calculator", Window: "Calculator"},
		{ID: "four", Application: "Notes", Window: "Calculator"},
	}
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{" calculator ", []int{0, 1}},
		{"OTHER   CALCULATOR", []int{2}},
		{"calc", nil},
		{"", nil},
		{"main", nil},
	} {
		if got := MatchApplications(tc.query, targets); !slices.Equal(got, tc.want) {
			t.Errorf("MatchApplications(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}
