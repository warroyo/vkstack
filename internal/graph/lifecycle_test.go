package graph

import (
	"testing"
	"time"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
	"github.com/warroyo/vkstack/internal/version"
)

func ms(t *testing.T, date string) int64 {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("bad test date %q: %v", date, err)
	}
	return parsed.UnixMilli()
}

func at(t *testing.T, date string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("bad test date %q: %v", date, err)
	}
	return parsed
}

// TestPublishedDatesOverrideStaleMatrixFlags is the case that motivated reading the
// lifecycle portal at all. VKr 1.32 went out of general support on 2026-02-28, and
// months later every 1.32.x build still carries genGuided in the interop matrix.
func TestPublishedDatesOverrideStaleMatrixFlags(t *testing.T) {
	r := Release{
		ProductKey: "vkr",
		Raw:        "1.32.10",
		// What the matrix says: fully supported.
		TechGuided: true, GenGuided: true,
		EOGSDate:            ms(t, "2026-02-28"),
		noTechnicalGuidance: true,
	}

	if got := r.Phase(); got != PhaseGeneral {
		t.Fatalf("matrix-only phase = %q, want the flags untouched at %q", got, PhaseGeneral)
	}
	if got := r.PhaseAt(at(t, "2026-08-11")); got != PhaseEndOfSupport {
		t.Errorf("phase after the published date = %q, want %q", got, PhaseEndOfSupport)
	}
	if got := r.PhaseSourceAt(at(t, "2026-08-11")); got != SourceLifecycle {
		t.Errorf("phase source = %q, want %q", got, SourceLifecycle)
	}
	if got := r.PhaseAt(at(t, "2026-01-01")); got != PhaseGeneral {
		t.Errorf("phase before the published date = %q, want %q", got, PhaseGeneral)
	}
}

// TestPlatformKeepsTechnicalGuidanceWithoutAnEOTGDate is the mirror case. vSphere's
// technical guidance is real and runs years past general support — 8.x ends general
// support on 2027-10-11 and technical guidance only in 2029 — so a missing end-of-
// technical-guidance date must not be read as "dead".
func TestPlatformKeepsTechnicalGuidanceWithoutAnEOTGDate(t *testing.T) {
	r := Release{
		ProductKey: "vcenter",
		Raw:        "8.0U3",
		TechGuided: true, GenGuided: true,
		EOGSDate: ms(t, "2027-10-11"),
		// noTechnicalGuidance deliberately false: vCenter has such a period.
	}

	if got := r.PhaseAt(at(t, "2027-11-01")); got != PhaseTechnicalGuidance {
		t.Errorf("phase just past general support = %q, want %q", got, PhaseTechnicalGuidance)
	}
	if got := r.PhaseAt(at(t, "2027-01-01")); got != PhaseGeneral {
		t.Errorf("phase before general support ends = %q, want %q", got, PhaseGeneral)
	}

	r.EOTGDate = ms(t, "2029-10-11")
	if got := r.PhaseAt(at(t, "2029-11-01")); got != PhaseEndOfSupport {
		t.Errorf("phase past technical guidance = %q, want %q", got, PhaseEndOfSupport)
	}
}

// TestMatrixEndOfSupportIsNeverUpgraded guards the one direction the dates must not win:
// a release the matrix has already retired stays retired, even though no date has passed.
func TestMatrixEndOfSupportIsNeverUpgraded(t *testing.T) {
	r := Release{
		ProductKey: "vcenter",
		Raw:        "6.7U3",
		TechGuided: false, GenGuided: false,
		EOGSDate: ms(t, "2020-01-01"),
	}
	if got := r.PhaseAt(at(t, "2026-08-11")); got != PhaseEndOfSupport {
		t.Errorf("phase = %q, want %q", got, PhaseEndOfSupport)
	}
}

func TestLifecycleLookupJoinsAcrossGranularities(t *testing.T) {
	rows := []store.Lifecycle{
		// VKr publishes a Kubernetes minor, plus older per-patch TKr rows.
		{ProductKey: "vkr", ReleaseKey: "1.32", EOGSDate: 1},
		{ProductKey: "vkr", ReleaseKey: "1.28.15", EOGSDate: 2},
		// vCenter publishes the update, not the patch letter.
		{ProductKey: "vcenter", ReleaseKey: "8.0U3", EOGSDate: 3},
		{ProductKey: "vcenter", ReleaseKey: "9.1.0.0", EOGSDate: 4},
		// NSX 9.x stops at the base build.
		{ProductKey: "nsx", ReleaseKey: "9.1.0.0", EOGSDate: 5},
		{ProductKey: "nsx", ReleaseKey: "4.2", EOGSDate: 6},
		// VKS matches verbatim.
		{ProductKey: "vks", ReleaseKey: "3.4.2+v1.33", EOGSDate: 7},
	}
	ix := lifecycleIndexes(rows)

	cases := []struct {
		name    string
		product string
		raw     string
		wantKey string
		wantHit bool
	}{
		{"vkr patch joins its minor line", "vkr", "1.32.10", "1.32", true},
		{"vkr exact minor", "vkr", "1.32", "1.32", true},
		{"vkr suffixed TKr string joins by its numeric run", "vkr", "1.28.15 (TKr 1.28.15 for vSphere 8.x)", "1.28.15", true},
		{"vkr line nobody published", "vkr", "1.99.1", "", false},
		{"vcenter patch letter joins its update", "vcenter", "8.0U3k", "8.0U3", true},
		{"vcenter build joins exactly", "vcenter", "9.1.0.0", "9.1.0.0", true},
		{"vcenter later build joins the base", "vcenter", "9.1.0.0300", "9.1.0.0", true},
		{"nsx build joins the base", "nsx", "9.1.0.0200", "9.1.0.0", true},
		{"nsx 4.x joins its line", "nsx", "4.2.4.1", "4.2", true},
		{"vks joins verbatim", "vks", "3.4.2+v1.33", "3.4.2+v1.33", true},
		{"vks near miss does not join", "vks", "3.4.1+v1.33", "", false},
		{"supervisor has no rows at all", "supervisor", "v1.32.9+vmware.2-fips-vsc9.1.0.0200", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := model.ByKey(tc.product)
			if !ok {
				t.Fatalf("unknown product %q", tc.product)
			}
			v := version.Parse(tc.raw, p.Scheme)
			got, hit := ix[tc.product].lookup(tc.raw, v, p.Lifecycle.Match)
			if hit != tc.wantHit {
				t.Fatalf("hit = %v, want %v (matched %q)", hit, tc.wantHit, got.ReleaseKey)
			}
			if hit && got.ReleaseKey != tc.wantKey {
				t.Errorf("joined %q, want %q", got.ReleaseKey, tc.wantKey)
			}
		})
	}
}

// TestKeepsExcludesOtherProductsRidingTheSameName is the filter that stops ESX inheriting
// a vSphere Replication date and NSX inheriting an HCX one. Both really do appear under
// the product's own productName in the portal.
func TestKeepsExcludesOtherProductsRidingTheSameName(t *testing.T) {
	cases := []struct {
		product     string
		description string
		want        bool
	}{
		{"esx", "VMware vSphere (ESXi)", true},
		{"esx", "VMware vSphere - Enterprise Plus", true},
		{"esx", "VMware Cloud Foundation", true},
		{"esx", "VMware vSphere Replication", false},
		{"esx", "VMware vSAN Data Protection", false},
		{"nsx", "VMware NSX", true},
		{"nsx", "VMware Cloud Foundation", true},
		{"nsx", "VMware HCX", false},
		{"nsx", "VMware Container Networking with Antrea", false},
		{"nsx", "VMware NSX Advanced Load Balancer", false},
		// vCenter writes the release into the description, so it can only match by prefix.
		{"vcenter", "VMware vCenter Server", true},
		{"vcenter", "VMware vCenter Server 8.0U2c", true},
		{"vcenter", "VMware vCenter Server Standard", true},
		{"vcenter", "VMware vSphere Replication", false},
		// Avi must match exactly or the separately versioned tooling comes with it.
		{"avi", "VMware Avi Load Balancer", true},
		{"avi", "VMware NSX Advanced Load Balancer", true},
		{"avi", "VMware Avi Load Balancer Conversion Tool", false},
		{"avi", "Avi Kubernetes Operator", false},
		// VKr filters by name alone: every row in that bucket is a VKr or TKr release.
		{"vkr", "VKr 1.32", true},
		{"vkr", "TKr 1.28.15 for vSphere 8.x", true},
	}

	for _, tc := range cases {
		p, ok := model.ByKey(tc.product)
		if !ok {
			t.Fatalf("unknown product %q", tc.product)
		}
		if got := p.Lifecycle.Keeps(tc.description); got != tc.want {
			t.Errorf("%s keeps %q = %v, want %v", tc.product, tc.description, got, tc.want)
		}
	}
}

// TestLoadStampsPublishedPhase checks the whole path: cached lifecycle rows joined onto
// cached releases, with the phase every view reads resolved at load.
func TestLoadStampsPublishedPhase(t *testing.T) {
	snap := fixture()
	snap.Lifecycle = []store.Lifecycle{
		{ProductKey: "vkr", ReleaseKey: "1.33", EOGSDate: ms(t, "2027-06-28")},
		{ProductKey: "vkr", ReleaseKey: "1.36", EOGSDate: ms(t, "2028-06-28")},
		{ProductKey: "vcenter", ReleaseKey: "8.0U3", EOGSDate: ms(t, "2027-10-11")},
	}
	for i := range snap.Releases {
		snap.Releases[i].TechGuided, snap.Releases[i].GenGuided = true, true
	}

	g, err := Load(snap, Options{Now: at(t, "2027-08-01")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// VKr 1.33.1 joins the 1.33 line, whose date has passed, and VKr has no technical
	// guidance period — so it is out of support despite the matrix flags.
	if r := g.Releases[52]; r.EffectivePhase() != PhaseEndOfSupport {
		t.Errorf("VKr 1.33.1 phase = %q, want %q (eogs %d, key %q)",
			r.EffectivePhase(), PhaseEndOfSupport, r.EOGSDate, r.LifecycleKey)
	}
	if r := g.Releases[52]; r.EffectivePhaseSource() != SourceLifecycle {
		t.Errorf("VKr 1.33.1 source = %q, want %q", r.EffectivePhaseSource(), SourceLifecycle)
	}
	// VKr 1.36.1 has not reached its date.
	if r := g.Releases[51]; r.EffectivePhase() != PhaseGeneral {
		t.Errorf("VKr 1.36.1 phase = %q, want %q", r.EffectivePhase(), PhaseGeneral)
	}
	// vCenter 8.0U3 has not either, and would only reach technical guidance when it does.
	if r := g.Releases[22]; r.EffectivePhase() != PhaseGeneral {
		t.Errorf("vCenter 8.0U3 phase = %q, want %q", r.EffectivePhase(), PhaseGeneral)
	}
	// The Supervisor has no lifecycle source at all and must be untouched.
	if r := g.Releases[31]; r.EOGSDate != 0 || r.EffectivePhaseSource() != SourceMatrix {
		t.Errorf("Supervisor picked up lifecycle data: eogs=%d source=%q", r.EOGSDate, r.EffectivePhaseSource())
	}
}
