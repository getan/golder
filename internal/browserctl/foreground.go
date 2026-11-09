package browserctl

import (
	"fmt"

	"github.com/getan/golder/internal/controlcap"
)

// Foreground is the single default-target rule for Agent and GUI. Only the
// conversation host's actual foreground is eligible; another safe background
// tab is not a substitute for a missing, closed or unsupported foreground.
func Foreground(info Info) (Tab, error) {
	if info.BrowserID == "" || info.ActiveTabID == "" {
		return Tab{}, fmt.Errorf("browser has no foreground tab")
	}
	for _, tab := range info.Tabs {
		if tab.ID == info.ActiveTabID {
			if tab.TargetID == "" || tab.URL == "" {
				return Tab{}, fmt.Errorf("foreground tab has no stable target or URL")
			}
			if err := controlcap.BrowserURL(tab.URL); err != nil {
				return Tab{}, err
			}
			return tab, nil
		}
	}
	return Tab{}, fmt.Errorf("foreground tab is no longer available")
}
