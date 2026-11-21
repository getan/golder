package computeruse

import (
	"context"
	"testing"
	"time"
)

func TestAXEnablementLifetime(t *testing.T) {
	p := axProcess{123, 10, 20}
	now := time.Now()
	a := axEnablement{process: p, hint: "accepted"}
	if a.needsRequest(p, now) || !a.needsRequest(axProcess{123, 10, 21}, now) {
		t.Fatal("enablement should follow process lifetime, not pid")
	}
	a.hint, a.retry = "not_accepted", now.Add(time.Second)
	if a.needsRequest(p, now) || !a.needsRequest(p, a.retry) {
		t.Fatal("transient opt-in failure should retry only after backoff")
	}
}

func TestAXRefreshAndTreeEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForAXRefresh(ctx, time.Hour) || !waitForAXRefresh(context.Background(), 0) {
		t.Fatal("settling must respect cancellation")
	}
	if observedWebContent(false, nil, false) != "" || observedWebContent(true, nil, true) != "not_observed_in_bounded_tree" || observedWebContent(true, nil, false) != "web_area_not_observed" || observedWebContent(true, []Element{{Role: "AXWebArea"}}, true) != "web_area_observed" {
		t.Fatal("tree evidence must distinguish absence, truncation and non-Electron apps")
	}
}
