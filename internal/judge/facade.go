package judge

import (
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/controlcap"
)

func facadeNetworkRequested(tool string, raw json.RawMessage) bool {
	return controlcap.Network(strings.ToLower(strings.TrimSpace(tool)), raw)
}
