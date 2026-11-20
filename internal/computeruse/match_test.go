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

func TestMatchApplicationsDoesNotWidenWindowIdentityWithAliases(t *testing.T) {
	targets := []Target{
		{ID: "one", Application: "DoubaoWork", Window: "Main"},
		{ID: "two", Application: "Doubao", Window: "Main"},
		{ID: "three", Application: "Notes", Window: "DoubaoWork"},
	}
	for _, query := range []string{"豆包work", "豆包工作", "doubao work"} {
		if got := MatchApplications(query, targets); got != nil {
			t.Errorf("MatchApplications(%q) = %v, want no window match", query, got)
		}
	}
	if got := MatchApplications("work", targets); got != nil {
		t.Errorf("partial name matched: %v", got)
	}
}
