package web

import (
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The palette is hand-written CSS with no build step and no linter in front of it, so the
// only thing standing between a nice-looking colour and an unreadable one is this test.
// It reads the stylesheet through AssetsFS so it checks the bytes that actually ship,
// embedded in the binary and copied into the static build, rather than a second copy.

// WCAG 2.1 SC 1.4.3: 4.5:1 for body text, 3:1 for large text and for the boundaries of
// user interface components (SC 1.4.11).
const (
	textAA = 4.5
	uiAA   = 3.0
)

// srgbToLinear undoes the sRGB transfer function for one channel, per WCAG 2.1's
// relative luminance definition.
func srgbToLinear(c float64) float64 {
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// relativeLuminance implements the WCAG 2.1 definition for a #rgb or #rrggbb colour.
func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		t.Fatalf("not a colour this test can measure: %q", hex)
	}
	ch := make([]float64, 3)
	for i := range ch {
		v, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			t.Fatalf("bad hex %q: %v", hex, err)
		}
		ch[i] = srgbToLinear(float64(v) / 255)
	}
	return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
}

func contrastRatio(t *testing.T, fg, bg string) float64 {
	t.Helper()
	a, b := relativeLuminance(t, fg), relativeLuminance(t, bg)
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}

var tokenRe = regexp.MustCompile(`--([a-z-]+)\s*:\s*(#[0-9a-fA-F]{3,8})\s*;`)

// palettes splits style.css at the dark media query and pulls the colour tokens out of
// each half. The light tokens are all declared in the first :root, above the query; the
// dark ones in the :root inside it, which ends where the next @media begins.
func palettes(t *testing.T) (light, dark map[string]string) {
	t.Helper()

	assets, err := AssetsFS()
	if err != nil {
		t.Fatalf("AssetsFS: %v", err)
	}
	b, err := fs.ReadFile(assets, "style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(b)

	const darkAt = "@media (prefers-color-scheme: dark)"
	i := strings.Index(css, darkAt)
	if i < 0 {
		t.Fatalf("no %s block in style.css", darkAt)
	}
	rest := css[i+len(darkAt):]
	end := strings.Index(rest, "@media")
	if end < 0 {
		end = len(rest)
	}

	collect := func(src string) map[string]string {
		m := map[string]string{}
		for _, sub := range tokenRe.FindAllStringSubmatch(src, -1) {
			m[sub[1]] = sub[2]
		}
		return m
	}
	return collect(css[:i]), collect(rest[:end])
}

// The pairings that actually occur in the stylesheet. Foreground/background here means
// "these two land on top of each other somewhere", not "these two are both colours".
//
// --hair is deliberately absent: it is a decorative divider, which SC 1.4.11 exempts.
// Every border that bounds a real control uses --edge, which is held to 3:1.
// --pick is absent as a background because nothing is ever painted on top of it — the
// picked states use --pick-soft as a fill and keep --ink as their text.
var pairs = []struct {
	fg, bg string
	min    float64
	where  string
}{
	{"ink", "surface", textAA, "body"},
	{"ink", "panel", textAA, ".node, .filter, .snippet pre"},
	{"muted", "surface", textAA, ".lede"},
	{"muted", "panel", textAA, "#tabs button, .layer-name, .section-label"},
	{"faint", "surface", textAA, ".meta, .disclaimer"},
	{"faint", "panel", textAA, ".node .detail, .layer-count, .map-caption"},

	// Dark mode lightens every accent, so these are the pairings that used to be a
	// hardcoded #fff and collapsed to roughly 2:1 there.
	{"on-accent", "signal", textAA, ".node.pinned, .segmented .segment.is-active"},
	{"on-accent", "tg", textAA, ".phase-tag.phase-technical-guidance"},
	{"on-accent", "eos", textAA, ".phase-tag.phase-end-of-support"},

	{"edge", "surface", uiAA, ".filter, .refresh-flag borders"},
	{"edge", "panel", uiAA, ".filter border against its own fill"},
}

func TestPaletteMeetsWCAGAA(t *testing.T) {
	light, dark := palettes(t)

	for _, theme := range []struct {
		name   string
		tokens map[string]string
	}{
		{"light", light},
		{"dark", dark},
	} {
		t.Run(theme.name, func(t *testing.T) {
			for _, p := range pairs {
				fg, ok := theme.tokens[p.fg]
				if !ok {
					t.Errorf("--%s is not defined in the %s palette", p.fg, theme.name)
					continue
				}
				bg, ok := theme.tokens[p.bg]
				if !ok {
					t.Errorf("--%s is not defined in the %s palette", p.bg, theme.name)
					continue
				}
				got := contrastRatio(t, fg, bg)
				if got < p.min {
					t.Errorf("--%s (%s) on --%s (%s) is %.2f:1, want at least %.1f:1 — %s",
						p.fg, fg, p.bg, bg, got, p.min, p.where)
				}
			}
		})
	}
}

// The three tiers have to stay distinguishable from each other, not just from the
// background, or the hierarchy the page leans on collapses into one flat grey.
func TestGreyRampStaysOrdered(t *testing.T) {
	light, dark := palettes(t)

	for name, tokens := range map[string]map[string]string{"light": light, "dark": dark} {
		t.Run(name, func(t *testing.T) {
			surface := relativeLuminance(t, tokens["surface"])
			var last float64
			for i, tier := range []string{"ink", "muted", "faint"} {
				d := math.Abs(relativeLuminance(t, tokens[tier]) - surface)
				if i > 0 && d >= last {
					t.Errorf("--%s is not closer to --surface than the tier above it "+
						"(%.4f vs %.4f); the ink/muted/faint hierarchy has inverted", tier, d, last)
				}
				last = d
			}
		})
	}
}

// A worked example of the maths, so a failure in the table above can be told apart from a
// bug in the measurement. Values are from the WCAG 2.1 definition.
func TestContrastRatioKnownValues(t *testing.T) {
	for _, c := range []struct {
		fg, bg string
		want   float64
	}{
		{"#000000", "#ffffff", 21},
		{"#ffffff", "#ffffff", 1},
		{"#777777", "#ffffff", 4.48},
	} {
		got := contrastRatio(t, c.fg, c.bg)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("contrast(%s, %s) = %s, want %.2f",
				c.fg, c.bg, fmt.Sprintf("%.2f", got), c.want)
		}
	}
}
