package appsession

import (
	"os"
	"strings"

	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
)

// SearchSettings holds the optional search-provider keys. They are stored as
// named credentials in the same encrypted file as provider keys.
type SearchSettings struct {
	Tavily   string
	Exa      string
	DeepSeek string
}

const (
	searchTavilyCredential   = "search-tavily"
	searchExaCredential      = "search-exa"
	searchDeepSeekCredential = "search-deepseek"
)

// LoadSearchSettings reads the keys and installs the process-local lookup the
// WebSearchTool uses. A missing credential file simply means no keys.
func LoadSearchSettings() (SearchSettings, error) {
	path := provider.CredentialFilePath()
	creds, err := provider.LoadCredentialFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			installSearchEnv(SearchSettings{})
			return SearchSettings{}, nil
		}
		return SearchSettings{}, err
	}
	settings := SearchSettings{
		Tavily:   creds[searchTavilyCredential],
		Exa:      creds[searchExaCredential],
		DeepSeek: creds[searchDeepSeekCredential],
	}
	installSearchEnv(settings)
	return settings, nil
}

// SaveSearchSettings stores each non-empty key and removes a key cleared by the
// person. The first configured provider wins; no key falls back to DuckDuckGo.
func SaveSearchSettings(edit SearchSettings) (SearchSettings, error) {
	path := provider.CredentialFilePath()
	edit.Tavily = strings.TrimSpace(edit.Tavily)
	edit.Exa = strings.TrimSpace(edit.Exa)
	edit.DeepSeek = strings.TrimSpace(edit.DeepSeek)
	for _, entry := range []struct{ name, value string }{
		{searchTavilyCredential, edit.Tavily},
		{searchExaCredential, edit.Exa},
		{searchDeepSeekCredential, edit.DeepSeek},
	} {
		if entry.value == "" {
			if err := provider.DeleteCredential(path, entry.name); err != nil {
				return SearchSettings{}, err
			}
			continue
		}
		if err := provider.SaveCredential(path, entry.name, entry.value); err != nil {
			return SearchSettings{}, err
		}
	}
	installSearchEnv(edit)
	return edit, nil
}

func installSearchEnv(settings SearchSettings) {
	keys := map[string]string{
		"TAVILY_API_KEY":   settings.Tavily,
		"EXA_API_KEY":      settings.Exa,
		"DEEPSEEK_API_KEY": settings.DeepSeek,
	}
	agenttool.SetSearchEnv(func(name string) string { return keys[name] })
}
