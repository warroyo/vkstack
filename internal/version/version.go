// Package version parses and orders the version strings the interop API publishes.
//
// Two schemes are needed, and they are never compared against each other:
//
//   - vSphere (ESX, vCenter) mixes two incompatible forms across majors —
//     "8.0U3k" for 8.x and "9.1.0.0300" for 9.x — plus a letter suffix that must rank
//     below the update number. A generic dotted-number parser sorts "8.0c" above
//     "8.0U3", which is wrong; hence the structured normalisation here.
//   - generic (Supervisor, VKS, VKr) is semver-ish with build metadata, where splitting
//     into numeric runs and comparing lexicographically is exactly right.
package version

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/warroyo/vkstack/internal/model"
)

// Version is a parsed, comparable version. Key is a list of numeric runs compared
// lexicographically; the vSphere scheme always produces exactly one run of six elements.
type Version struct {
	Raw    string
	Scheme model.Scheme
	Key    [][]int
	// Parsed reports whether the string matched a known form. Unparsed versions still
	// get a best-effort Key from their numeric runs so they sort somewhere sensible.
	Parsed bool
}

// Major returns the leading number: 8 for "8.0U3k", 9 for "9.1.0.0300", 3 for "3.7.0+v1.36".
//
// Parse always leaves at least one run behind, even for a string it did not recognise, so
// the guard here only catches a zero Version that never went through Parse at all.
func (v Version) Major() int {
	if len(v.Key) == 0 || len(v.Key[0]) == 0 {
		return 0
	}
	return v.Key[0][0]
}

var (
	// "8.0", "8.0c", "8.0.0c", "8.0U1", "8.0U3k" — up to three dotted numbers, an
	// optional U<n> update, and an optional single letter.
	vsphereUForm = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?(?:[Uu](\d+))?([a-zA-Z])?$`)
	// "9.0.0.0", "9.1.0.0300" — exactly four dotted numbers, the last being the build.
	vsphereDotted4 = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)\.(\d+)$`)
	// A maximal dotted-numeric run, e.g. "1.33.9" inside "v1.33.9+vmware.3".
	numericRun = regexp.MustCompile(`\d+(?:\.\d+)*`)
)

// Parse parses s under the given scheme.
func Parse(s string, scheme model.Scheme) Version {
	s = strings.TrimSpace(s)
	if scheme == model.SchemeVSphere {
		return parseVSphere(s)
	}
	return parseGeneric(s)
}

// parseVSphere normalises to a single run of [major, minor, patch, update, letter, build].
//
// The ordering that matters and that a naive parser gets wrong:
//
//	8.0 < 8.0c < 8.0U1 < 8.0U1a < 8.0U2f < 8.0U3 < 8.0U3k < 9.0.0.0 < 9.1.0.0300
//
// ESX writes "8.0c" where vCenter writes "8.0.0c" for the analogous release; both land
// on the same key.
func parseVSphere(s string) Version {
	v := Version{Raw: s, Scheme: model.SchemeVSphere}

	if m := vsphereDotted4.FindStringSubmatch(s); m != nil {
		v.Key = [][]int{{atoi(m[1]), atoi(m[2]), atoi(m[3]), 0, 0, atoi(m[4])}}
		v.Parsed = true
		return v
	}
	if m := vsphereUForm.FindStringSubmatch(s); m != nil {
		v.Key = [][]int{{atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4]), letterOrdinal(m[5]), 0}}
		v.Parsed = true
		return v
	}

	// Unknown form: fall back to the leading numeric run so it still sorts by major.
	if run := numericRun.FindString(s); run != "" {
		parts := splitRun(run)
		key := make([]int, 6)
		copy(key, parts)
		v.Key = [][]int{key}
	} else {
		v.Key = [][]int{{0, 0, 0, 0, 0, 0}}
	}
	return v
}

// parseGeneric splits into every maximal dotted-numeric run, in order.
//
//	3.7.0+v1.36                     -> [[3 7 0] [1 36]]
//	v1.33.9+vmware.3-fips-vsc0.1.15 -> [[1 33 9] [3] [0 1 15]]
//
// Leading "v" and other text fall out as separators. Trailing prose on old VKr entries
// ("1.20.2 (TKr 1.20.2 for vSphere 7.x)") does not affect ordering because the first run
// dominates. No letter-ordinal rule here: these products have no "8.0c"-style suffixes,
// and adding one would reintroduce the vSphere bug.
func parseGeneric(s string) Version {
	v := Version{Raw: s, Scheme: model.SchemeGeneric}
	for _, run := range numericRun.FindAllString(s, -1) {
		v.Key = append(v.Key, splitRun(run))
	}
	v.Parsed = len(v.Key) > 0
	if !v.Parsed {
		v.Key = [][]int{{0}}
	}
	return v
}

// Compare returns -1, 0 or 1. Runs are compared in order, element by element; where one
// is a prefix of the other, the shorter sorts lower ("8.0" < "8.0.1").
func Compare(a, b Version) int {
	for i := 0; i < len(a.Key) && i < len(b.Key); i++ {
		if c := compareRun(a.Key[i], b.Key[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Key) < len(b.Key):
		return -1
	case len(a.Key) > len(b.Key):
		return 1
	}
	return 0
}

// Less reports whether a sorts before b.
func Less(a, b Version) bool { return Compare(a, b) < 0 }

func compareRun(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// K8sMinor returns the Kubernetes minor version a release tracks, using the product's
// declared run index: run 0 for Supervisor ("v1.32.9+...") and VKr ("1.36.1"), run 1 for
// VKS ("3.7.0+v1.36"). Reports false for ESX and vCenter, and for versions too short to
// carry one (e.g. VKS "3.1.0", which omits the "+v1.xx" tail).
func K8sMinor(v Version, p model.Product) (int, bool) {
	if p.K8sMinorRun < 0 || p.K8sMinorRun >= len(v.Key) {
		return 0, false
	}
	run := v.Key[p.K8sMinorRun]
	if len(run) < 2 {
		return 0, false
	}
	return run[1], true
}

// letterOrdinal maps "" to 0 and a..z to 1..26, so an absent suffix sorts below "a".
func letterOrdinal(s string) int {
	if s == "" {
		return 0
	}
	return int(strings.ToLower(s)[0]-'a') + 1
}

func splitRun(run string) []int {
	parts := strings.Split(run, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		out[i] = atoi(p)
	}
	return out
}

func atoi(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
