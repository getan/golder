package provider

// Built-in model catalogs for the four gateways most likely to be the first
// one a new install talks to. They are used ONLY when the live /models request
// fails and no disk cache exists — the case that matters on a restricted
// network, where the first fetch can time out and leave `/model` empty.
//
// Snapshot taken 2026-10-20 from a working install. Model lineups move; these
// lists are a floor, not an authority. A fallback is never written to the disk
// cache, so the next run still tries the live endpoint and replaces it when the
// network cooperates.

// FallbackModelCatalog returns a copy of the bundled list for providerName, or
// nil when that provider has no snapshot. A copy keeps callers from mutating
// the table shared by every session.
func FallbackModelCatalog(providerName string) []string {
	ids, ok := fallbackModelCatalog[providerName]
	if !ok || len(ids) == 0 {
		return nil
	}
	return append([]string(nil), ids...)
}

var fallbackModelCatalog = map[string][]string{
	"commandcode": {
		"MiniMaxAI/MiniMax-M2.5", "MiniMaxAI/MiniMax-M2.7", "MiniMaxAI/MiniMax-M3",
		"Qwen/Qwen3.6-Max-Preview", "Qwen/Qwen3.6-Plus", "Qwen/Qwen3.7-Flash",
		"Qwen/Qwen3.7-Max", "Qwen/Qwen3.7-Plus", "Qwen/Qwen3.8-27B",
		"Qwen/Qwen3.8-Flash", "Qwen/Qwen3.8-Max", "Qwen/Qwen3.8-Max-0902",
		"Qwen/Qwen3.8-Max-Prime", "Qwen/Qwen3.8-Omni-Flash",
		"claude-fable-5", "claude-fable-5-1", "claude-haiku-4-5-20251001",
		"claude-haiku-5-5", "claude-opus-4-7", "claude-opus-4-8",
		"claude-opus-5", "claude-opus-5-5", "claude-sonnet-4-6",
		"claude-sonnet-5", "claude-sonnet-5-5",
		"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-flash-fast",
		"deepseek/deepseek-v4-flash-vision-exp", "deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash-fast",
		"google/gemini-3.1-flash-lite", "google/gemini-3.5-flash",
		"google/gemini-3.5-flash-lite", "google/gemini-3.6-flash",
		"google/gemini-3.7-flash", "google/gemini-3.8-flash",
		"google/gemini-4-argon", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-mini",
		"gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra",
		"gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol",
		"inclusionai/ling-3.1-flash", "meituan/LongCat-2.0",
		"meta/muse-spark-1.1", "meta/muse-spark-1.2",
		"meta/muse-spark-1.2-contributor", "meta/muse-spark-1.3",
		"meta/muse-spark-1.3-contributor", "meta/muse-spark-open",
		"meta/muse-wonder-1.0", "mistral/mistral-large-4",
		"moonshotai/Kimi-K2.5", "moonshotai/Kimi-K2.6",
		"moonshotai/Kimi-K2.7-Code", "moonshotai/Kimi-K2.7-Code-Highspeed",
		"moonshotai/Kimi-K3", "nvidia/nemotron-3-ultra-550b-a55b",
		"nvidia/nemotron-3.5-super-vl", "poolside/laguna-s-2.1-free",
		"reflection/beam:free", "sakana/fugu-ultra", "stealth/cat-jam:free",
		"stealth/glyph-cluster:free", "stepfun/Step-3.5-Flash",
		"stepfun/Step-3.7-Flash", "stepfun/Step-5-Preview",
		"tencent/hy3-paid", "tencent/hy4-preview",
		"thinkingmachines/inkling", "thinkingmachines/inkling-small",
		"xai/grok-4.5", "xai/grok-4.6", "xai/grok-4.7",
		"xiaomi/mimo-v2.5", "xiaomi/mimo-v2.5-pro", "xiaomi/mimo-v2.6-flash",
		"xiaomi/mimo-v2.6-pro", "xiaomi/mimo-v2.6-pro-ultraspeed",
		"z-ai/glm-5.3-flash", "z-ai/glm-5.3-flashx", "zai-org/GLM-5",
		"zai-org/GLM-5.1", "zai-org/GLM-5.2", "zai-org/GLM-5.2-Fast",
		"zai-org/GLM-5.3",
	},
	"opencode-go": {
		"beam-free", "claude-haiku-5-5", "deepseek-v4-pro",
		"deepseek-v4.1-flash", "gemini-4-argon-contributor", "glm-5.1",
		"glm-5.2", "glm-5.3", "glm-5.3-flash", "gpt-5.6-luna",
		"gpt-6-luna", "grok-4.6", "grok-4.7", "hy3", "hy4-preview",
		"kimi-k2.6", "kimi-k2.7-code", "kimi-k3", "longcat-2.0",
		"longcat-2.5-preview-free", "mimo-v2.5", "mimo-v2.5-pro",
		"mimo-v2.6-flash", "mimo-v2.6-pro", "minimax-m2.5",
		"minimax-m2.7", "minimax-m3", "minimax-m3.1-flash-free",
		"muse-spark-1.2-contributor", "muse-spark-1.3-contributor",
		"omen-alpha", "qwen3.6-plus", "qwen3.7-max", "qwen3.7-plus",
		"qwen3.8-flash", "qwen3.8-max", "step-5-preview",
	},
	"deepseek": {
		"deepseek-flash", "deepseek-v4-pro",
	},
	"openai": {
		"codex-auto-review", "gpt-4o-audio-preview", "gpt-4o-realtime-preview",
		"gpt-5.2", "gpt-5.2-2025-12-11", "gpt-5.2-chat-latest",
		"gpt-5.2-pro", "gpt-5.2-pro-2025-12-11", "gpt-5.3-codex-spark",
		"gpt-5.4", "gpt-5.4-2026-03-05", "gpt-5.4-mini", "gpt-5.6",
		"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6",
		"gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol",
		"gpt-image-1", "gpt-image-1.5", "gpt-image-2",
		"gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-reserve",
	},
}
