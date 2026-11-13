package mygoui

import (
	"context"
	"testing"

	"github.com/getan/golder/gui/appsession"
)

func TestGitDraftAndReviewViewBelongToConversation(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	id := a.status.SessionID
	a.gitState(id).commitMessage = "first conversation commit"
	a.changesOpen, a.changesView, a.diffSel = true, appsession.ChangesStaged, 2
	second, err := a.hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	a.activateSession(second)
	if a.gitState(a.status.SessionID).commitMessage != "" || a.currentChangesView() != appsession.ChangesUnstaged || a.changesOpen {
		t.Fatal("Git UI leaked into second conversation")
	}
	a.gitState(a.status.SessionID).commitMessage = "second conversation commit"
	a.activateSession(first)
	if a.gitState(id).commitMessage != "first conversation commit" || a.currentChangesView() != appsession.ChangesStaged || !a.changesOpen || a.diffSel != 2 {
		t.Fatal("first conversation Git view/draft was not restored")
	}
}
