package mygoui

import (
	"context"
	"time"

	"github.com/getan/golder/gui/appsession"
)

type gitPanelState struct {
	commitMessage string
	busy          bool
	operation     string
	result        string
	err           string
	open          bool
	view          appsession.ChangeView
	selected      int
}

func (a *App) gitState(id string) *gitPanelState {
	if a.gitStates == nil {
		a.gitStates = map[string]*gitPanelState{}
	}
	if a.gitStates[id] == nil {
		a.gitStates[id] = &gitPanelState{}
	}
	return a.gitStates[id]
}

func (a *App) rememberGitView(id string) {
	s := a.gitState(id)
	s.open, s.view, s.selected = a.changesOpen, a.changesView, a.diffSel
}

func (a *App) restoreGitView(id string) {
	a.closeChangesPanel()
	a.diffRequest++
	a.changes, a.fileDiff = appsession.Changes{}, appsession.FileDiff{}
	a.changesErr, a.diff = "", nil
	s := a.gitState(id)
	a.changesOpen, a.changesView, a.diffSel = s.open, s.view, s.selected
}

func (a *App) gitDisabled() bool {
	return a.ctrl == nil || a.status.Running || a.status.ReadOnly || a.status.Permission == "read-only" || a.gitState(a.status.SessionID).busy
}

func (a *App) runGitAction(action, path string) {
	if a.gitDisabled() || a.changes.NotGitRepo || a.changes.RepoRoot == "" {
		return
	}
	ctrl, id, project, win := a.ctrl, a.status.SessionID, a.project, a.win
	s := a.gitState(id)
	message := s.commitMessage
	s.busy, s.operation, s.result, s.err = true, action, "", ""
	request := appsession.GitRequest{SessionID: id, Project: project, RepoRoot: a.changes.RepoRoot, Action: action, Path: path, Message: message}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		text, err := ctrl.GitOperation(ctx, request)
		apply := func() {
			s.busy, s.operation = false, ""
			if err != nil {
				s.err = err.Error()
			} else {
				s.result = text
				if action == "commit" && s.commitMessage == message {
					s.commitMessage = ""
				}
			}
			// Result/draft stay with their conversation. Never refresh or show a
			// background repository's result in a newly selected conversation.
			if a.ctrl == ctrl && a.status.SessionID == id && a.project == project {
				a.refreshChanges()
			}
		}
		if win == nil {
			apply()
		} else {
			win.Update(apply)
		}
	}
	if win == nil {
		run()
	} else {
		go run()
	}
}
