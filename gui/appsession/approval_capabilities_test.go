package appsession

import (
	"testing"

	"github.com/getan/golder/internal/judge"
)

func TestApprovalCacheKeysTheActualCapabilities(t *testing.T) {
	a := judge.ApprovalRequest{Tool: "bash", Summary: "go test ./...", Needs: []judge.Need{{Cap: judge.CapReadOutside, Path: "/cache/modules"}}}
	b := a
	b.Needs = []judge.Need{{Cap: judge.CapWriteOutside, Path: "/cache/modules"}}
	if approvalKey(a) == approvalKey(b) {
		t.Fatal("write capability reused read approval")
	}
	b.Needs = []judge.Need{{Cap: judge.CapReadOutside, Path: "/different/cache"}}
	if approvalKey(a) == approvalKey(b) {
		t.Fatal("different cache reused approval")
	}
}
