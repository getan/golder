package provider

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/openai/openai-go/responses"

	"github.com/getan/golder/internal/agentcore"
)

// Hosted web_search for the Responses driver, with probe-and-fallback.
//
// Response-family models declare the hosted web_search tool; endpoints that do
// not support it (a gateway/model combination such as DeepSeek behind a proxy
// that dropped the feature) fail the request with a capability-shaped 400.
// The driver then retries once with the local websearch function tool instead,
// and records the negative so later turns skip the probe. The cache is
// process-local only: support can appear at any time, and a stale negative
// would hide it. A wasted probe costs one 400 per process start.

// localWebSearchToolName is the golder function tool hidden while the hosted
// variant is declared, so the model faces exactly one search path per attempt.
const localWebSearchToolName = "websearch"

// hostedSearchUnsupported records (provider, model) pairs whose endpoint
// rejected the hosted web_search tool, keyed by provider+"\x00"+model.
var hostedSearchUnsupported sync.Map

func hostedSearchCacheKey(provider, model string) string {
	return provider + "\x00" + strings.TrimSpace(model)
}

// markHostedSearchUnsupported records a negative probe result.
func markHostedSearchUnsupported(provider, model string) {
	hostedSearchUnsupported.Store(hostedSearchCacheKey(provider, model), true)
}

// hostedSearchKnownUnsupported reports a cached negative probe result.
func hostedSearchKnownUnsupported(provider, model string) bool {
	_, ok := hostedSearchUnsupported.Load(hostedSearchCacheKey(provider, model))
	return ok
}

// clearHostedSearchUnsupported drops a cached negative (tests; replumbing).
func clearHostedSearchUnsupported(provider, model string) {
	hostedSearchUnsupported.Delete(hostedSearchCacheKey(provider, model))
}

// isHostedSearchUnsupportedErr matches capability-shaped rejections: HTTP 400
// with the body naming web_search (unknown tool type). Anything else (auth,
// rate limit, 5xx) is a real failure and must surface, never degrade.
func isHostedSearchUnsupportedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "400") {
		return false
	}
	return strings.Contains(msg, "web_search") ||
		(strings.Contains(msg, "unknown tool") || strings.Contains(msg, "unsupported"))
}

// hostedSearchToolParam builds the hosted web_search declaration. The SDK at
// this version models the preview variant, which gateways accept widely.
func hostedSearchToolParam() responses.ToolUnionParam {
	return responses.ToolParamOfWebSearchPreview(responses.WebSearchToolTypeWebSearchPreview)
}

// webSearchServerCall maps a completed web_search_call output item into a
// server-executed ToolCallContent for display + native history replay. The
// query (search actions) rides in Arguments; open_page/find actions keep their
// action type so the card still reads sensibly.
func webSearchServerCall(item responses.ResponseFunctionWebSearch) (agentcore.ToolCallContent, bool) {
	if item.Type != "web_search_call" || strings.TrimSpace(item.ID) == "" {
		return agentcore.ToolCallContent{}, false
	}
	args := map[string]any{}
	switch strings.ToLower(strings.TrimSpace(item.Action.Type)) {
	case "", "search":
		args["query"] = strings.TrimSpace(item.Action.Query)
	case "open_page":
		args["action"] = "open_page"
		args["url"] = strings.TrimSpace(item.Action.URL)
	case "find":
		args["action"] = "find"
		args["pattern"] = strings.TrimSpace(item.Action.Pattern)
	default:
		args["action"] = strings.TrimSpace(item.Action.Type)
	}
	raw, _ := json.Marshal(args)
	return agentcore.NewServerToolCallContent(strings.TrimSpace(item.ID), "web_search", json.RawMessage(raw)), true
}

// webSearchCallReplayParam rebuilds a web_search_call history item from a
// server call's Arguments. Only search actions round-trip (the SDK constructor
// covers search/open_page/find params, but golder records enough to rebuild
// search faithfully; anything else is dropped from replay while the answer
// text keeps its citations).
func webSearchCallReplayParam(call agentcore.ToolCallContent) (responses.ResponseInputItemUnionParam, bool) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return responses.ResponseInputItemUnionParam{}, false
	}
	if strings.TrimSpace(args.Query) == "" || strings.TrimSpace(call.ID) == "" {
		return responses.ResponseInputItemUnionParam{}, false
	}
	action := responses.ResponseFunctionWebSearchActionSearchParam{Query: strings.TrimSpace(args.Query)}
	return responses.ResponseInputItemParamOfWebSearchCall(
		action, strings.TrimSpace(call.ID), responses.ResponseFunctionWebSearchStatusCompleted), true
}

// appendSearchCitations appends a Sources block built from the url_citation
// annotations on the response's message outputs, so links survive in the
// transcript, session history, and replays. Duplicates collapse, order kept.
// citedURLs scans prior assistant text for already-surfaced links, so a
// follow-up turn does not re-print the same Sources block: the URLs stay
// visible in history without duplicating every round.
func citedURLs(msgs agentcore.MessageList) map[string]bool {
	seen := map[string]bool{}
	for _, m := range msgs {
		am, ok := m.(agentcore.AssistantMessage)
		if !ok {
			continue
		}
		for _, c := range am.Content {
			tc, ok := c.(agentcore.TextContent)
			if !ok {
				continue
			}
			for _, f := range strings.Fields(tc.Text) {
				u := strings.Trim(f, ".,;:!?\"'()[]")
				if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
					seen[u] = true
				}
			}
		}
	}
	return seen
}

func appendSearchCitations(content agentcore.ContentList, output []responses.ResponseOutputItemUnion, seen map[string]bool) agentcore.ContentList {
	type cite struct{ title, url string }
	var cites []cite
	if seen == nil {
		seen = map[string]bool{}
	}
	for _, item := range output {
		msg, ok := item.AsAny().(responses.ResponseOutputMessage)
		if !ok || len(msg.Content) == 0 {
			continue
		}
		for _, c := range msg.Content {
			text := c.AsOutputText()
			if text.Type != "output_text" {
				continue
			}
			for _, a := range text.Annotations {
				u := a.AsURLCitation()
				if u.Type != "url_citation" || strings.TrimSpace(u.URL) == "" {
					continue
				}
				key := strings.TrimSpace(u.URL)
				if seen[key] {
					continue
				}
				seen[key] = true
				cites = append(cites, cite{title: strings.TrimSpace(u.Title), url: key})
			}
		}
	}
	if len(cites) == 0 {
		return content
	}
	var b strings.Builder
	b.WriteString("Sources:")
	for _, c := range cites {
		b.WriteString("\n- ")
		if c.title != "" {
			b.WriteString(c.title + ": ")
		}
		b.WriteString(c.url)
	}
	return append(content, agentcore.NewTextContent(b.String()))
}
