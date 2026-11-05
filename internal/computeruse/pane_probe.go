package computeruse

import "strings"

func paneGraphicsReply(reply string) bool {
	for {
		_, rest, found := strings.Cut(reply, "\x1b_G")
		if !found {
			return false
		}
		payload, next, found := strings.Cut(rest, "\x1b\\")
		if !found {
			return false
		}
		metadata, result, found := strings.Cut(payload, ";")
		if found && strings.Contains(","+metadata+",", ",i=31,") && strings.TrimSpace(result) == "OK" {
			return true
		}
		reply = next
	}
}
