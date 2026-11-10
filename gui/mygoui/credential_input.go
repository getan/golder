package mygoui

import (
	"strings"

	"github.com/getan/golder/gui/appsession"
)

// The mask is a placeholder only. It never enters the write-only edit fields,
// so saving an unchanged form cannot overwrite a credential with the mask.
const credentialMaskPlaceholder = "••••••••"

func credentialPlaceholder(available bool, emptyHint string) string {
	if available {
		return credentialMaskPlaceholder
	}
	return emptyHint
}

func providerCredentialAvailable(settings appsession.ProviderSettings, edit appsession.ProviderSettingsEdit) bool {
	return settings.Credential.Available && !edit.RemoveCredential &&
		strings.EqualFold(strings.TrimSpace(settings.Provider), strings.TrimSpace(edit.Provider)) &&
		strings.TrimSpace(settings.Credential.Name) == strings.TrimSpace(edit.CredentialName)
}

func larkCredentialAvailable(settings appsession.LarkExtensionSettings, edit appsession.LarkExtensionEdit) bool {
	name := strings.TrimSpace(settings.CredentialName)
	return settings.CredentialAvailable && name != "" && name == strings.TrimSpace(edit.CredentialName)
}
