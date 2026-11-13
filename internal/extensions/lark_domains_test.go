package extensions

import (
	"strings"
	"testing"
)

func TestLarkExtendedDomainsAreOptional(t *testing.T) {
	got, err := normalizeLarkDomains([]string{"calendar", "task", "wiki"})
	if err != nil || strings.Join(got, ",") != "calendar,task,wiki" {
		t.Fatalf("extended domains = %v, %v", got, err)
	}
	defaults, err := normalizeLarkDomains(nil)
	if err != nil || strings.Join(defaults, ",") != "docs,drive,im,sheets,base" {
		t.Fatalf("default domains expanded without opt-in: %v, %v", defaults, err)
	}
}
