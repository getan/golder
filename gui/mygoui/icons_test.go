package mygoui

import (
	"strings"
	"testing"
)

func TestApprovalHandIsOutlinedMonochromeSVG(t *testing.T) {
	if !strings.Contains(approvalHandSVG, `fill="none"`) || !strings.Contains(approvalHandSVG, `stroke="currentColor"`) {
		t.Fatalf("approval hand is not an outlined theme-colored SVG: %s", approvalHandSVG)
	}
	if strings.Contains(approvalHandSVG, `fill="currentColor"`) {
		t.Fatalf("approval hand became a solid silhouette: %s", approvalHandSVG)
	}
}
