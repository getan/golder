package provider

import "strings"

// CompareModelVersion orders two ids of the same family by version, negative when
// a is older. It reads the numeric runs of each id, so "deepseek-v4.1-flash" >
// "deepseek-v4", "gpt-5.6" > "gpt-5", "glm-5.3" > "glm-4". A model that carries
// a version beats one that does not, and when the numbers tie the shorter id
// wins — the plain release is the family's name, its suffixed variants are
// derivatives ("gpt-5" > "gpt-5-mini"). It is a naming heuristic, used only to
// pick the representative model a family probe asks about.
func CompareModelVersion(a, b string) int {
	va, vb := versionTuple(a), versionTuple(b)
	for i := 0; i < len(va) || i < len(vb); i++ {
		var x, y int
		if i < len(va) {
			x = va[i]
		}
		if i < len(vb) {
			y = vb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if len(va) != len(vb) {
		if len(va) > len(vb) {
			return 1
		}
		return -1
	}
	switch {
	case len(a) < len(b):
		return 1
	case len(a) > len(b):
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// versionTuple extracts the numeric runs of a model id, in order.
func versionTuple(id string) []int {
	var out []int
	current, have := 0, false
	for _, r := range strings.ToLower(id) {
		if r >= '0' && r <= '9' {
			current = current*10 + int(r-'0')
			have = true
			continue
		}
		if have {
			out = append(out, current)
			current, have = 0, false
		}
	}
	if have {
		out = append(out, current)
	}
	return out
}

// newestInFamily returns the newest model of this model's family that the
// provider's cached catalog knows, or the requested model when the catalog has
// nothing to say. The family probe asks about that model: a vendor ships one
// line, so the newest member is the one whose wire means something, and its
// answer is stored under the family for every sibling.
func newestInFamily(spec ProviderSpec, model, baseURL string, env func(string) string) string {
	fam := ModelFamily(model)
	if fam == "" {
		return model
	}
	ids, _ := CachedModelCatalog(ModelCatalogCacheKey(spec.Name, ResolveBaseURL(spec, baseURL, env)))
	newest := ""
	for _, id := range ids {
		if ModelFamily(id) != fam {
			continue
		}
		if newest == "" || CompareModelVersion(id, newest) > 0 {
			newest = id
		}
	}
	if newest == "" {
		return model
	}
	return newest
}
