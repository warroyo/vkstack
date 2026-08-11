package graph

import (
	"sort"
	"strconv"
	"strings"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
	"github.com/warroyo/vkstack/internal/version"
)

// lifecycleIndex holds one product's published support dates, keyed by the portal's own
// release string.
//
// It is a separate index rather than a column on the release because the two sources do
// not agree on granularity: the portal publishes VKr per Kubernetes minor and ESX per
// major line, while interop publishes per build. The join is therefore a lookup with
// fallbacks, not an equality.
type lifecycleIndex struct {
	byKey map[string]store.Lifecycle
	// byNormalized absorbs the cosmetic disagreements between the two sources, where
	// they mean the same release and spell it differently.
	byNormalized map[string]store.Lifecycle
	// keys is every release key, longest first, so a prefix search finds the most
	// specific row rather than the first plausible one.
	keys []string
}

func newLifecycleIndex(rows []store.Lifecycle) *lifecycleIndex {
	ix := &lifecycleIndex{
		byKey:        make(map[string]store.Lifecycle, len(rows)),
		byNormalized: make(map[string]store.Lifecycle, len(rows)),
	}
	for _, r := range rows {
		ix.byKey[r.ReleaseKey] = r
		ix.byNormalized[normalizeJoinKey(r.ReleaseKey)] = r
		ix.keys = append(ix.keys, r.ReleaseKey)
	}
	sort.Slice(ix.keys, func(i, j int) bool {
		if len(ix.keys[i]) != len(ix.keys[j]) {
			return len(ix.keys[i]) > len(ix.keys[j])
		}
		return ix.keys[i] < ix.keys[j]
	})
	return ix
}

// lookup finds the published dates for one release, or reports that the portal says
// nothing about it.
//
// Exact is always tried first, whatever the product's declared fallback: where the two
// sources do use the same string it is the only answer that cannot be wrong.
func (ix *lifecycleIndex) lookup(raw string, v version.Version, m model.Match) (store.Lifecycle, bool) {
	if ix == nil {
		return store.Lifecycle{}, false
	}
	if l, ok := ix.byKey[raw]; ok {
		return l, true
	}
	// The same release, spelled differently: interop publishes VKS 3.6.3 as
	// "3.6.3+1.35" where the portal writes "3.6.3+v1.35".
	if l, ok := ix.byNormalized[normalizeJoinKey(raw)]; ok {
		return l, true
	}

	switch m {
	case model.MatchMinor:
		// The version's own numeric run, so "1.28.15 (TKr 1.28.15 for vSphere 8.x)"
		// still finds the row published as "1.28.15".
		if full := runString(v, 0); full != "" && full != raw {
			if l, ok := ix.byKey[full]; ok {
				return l, true
			}
		}
		if minor := runString(v, 2); minor != "" {
			if l, ok := ix.byKey[minor]; ok {
				return l, true
			}
		}
	case model.MatchPrefix:
		for _, k := range ix.keys {
			if k != "" && strings.HasPrefix(raw, k) {
				return ix.byKey[k], true
			}
		}
	}
	return store.Lifecycle{}, false
}

// normalizeJoinKey drops a "v" written immediately before a number, which is the only
// cosmetic difference seen between the two sources' release strings.
//
// It is deliberately that narrow. The "v" in "vmware", "vsc9" and "vSphere" is followed
// by a letter and survives, so this cannot quietly merge two genuinely different builds.
func normalizeJoinKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c == 'v' || c == 'V') && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// runString renders a version's first numeric run as a dotted string, truncated to n
// components. n of 0 means the whole run: "1.32.10" for VKr 1.32.10, "1.32" at n=2.
func runString(v version.Version, n int) string {
	if len(v.Key) == 0 || len(v.Key[0]) == 0 {
		return ""
	}
	run := v.Key[0]
	if n > 0 && n < len(run) {
		run = run[:n]
	}
	parts := make([]string, len(run))
	for i, p := range run {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ".")
}

// lifecycleIndexes groups cached lifecycle rows by product key.
func lifecycleIndexes(rows []store.Lifecycle) map[string]*lifecycleIndex {
	byProduct := map[string][]store.Lifecycle{}
	for _, r := range rows {
		byProduct[r.ProductKey] = append(byProduct[r.ProductKey], r)
	}
	out := make(map[string]*lifecycleIndex, len(byProduct))
	for key, rs := range byProduct {
		out[key] = newLifecycleIndex(rs)
	}
	return out
}
