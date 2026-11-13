package extensions

import (
	"strings"
	"testing"
)

func TestLarkDomainsDefaultToAllServices(t *testing.T) {
	defaults, err := normalizeLarkDomains(nil)
	if err != nil || strings.Join(defaults, ",") != "docs,drive,im,sheets,base,calendar,task,wiki" {
		t.Fatalf("default domains = %v, %v", defaults, err)
	}
	explicit, err := normalizeLarkDomains([]string{"docs", "im"})
	if err != nil || strings.Join(explicit, ",") != "docs,im" {
		t.Fatalf("explicit domains = %v, %v", explicit, err)
	}
}
