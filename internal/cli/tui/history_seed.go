package tui

import (
	"github.com/getan/golder/internal/prompthistory"
	"github.com/getan/golder/internal/session"
)

const seedMarkerSuffix = prompthistory.SeedMarkerSuffix

func seedPromptHistoryFromSessions(store *session.Store, path string) {
	prompthistory.SeedFromSessions(store, path)
}
