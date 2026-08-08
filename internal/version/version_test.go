package version

import (
	"testing"

	"github.com/warroyo/interop-visualizer/internal/model"
)

// assertAscending checks that the given versions are in strictly increasing order.
func assertAscending(t *testing.T, scheme model.Scheme, versions []string) {
	t.Helper()
	for i := 1; i < len(versions); i++ {
		lo := Parse(versions[i-1], scheme)
		hi := Parse(versions[i], scheme)
		if c := Compare(lo, hi); c >= 0 {
			t.Errorf("expected %q < %q, got compare = %d (keys %v vs %v)",
				versions[i-1], versions[i], c, lo.Key, hi.Key)
		}
	}
}

func TestVSphereOrdering(t *testing.T) {
	// Spans both 8.x U-form and 9.x dotted-4 form, which is where a single naive
	// parser falls over.
	assertAscending(t, model.SchemeVSphere, []string{
		"8.0",
		"8.0.0a",
		"8.0.0c",
		"8.0U1",
		"8.0U1a",
		"8.0U1e",
		"8.0U2",
		"8.0U2f",
		"8.0U3",
		"8.0U3a",
		"8.0U3k",
		"9.0.0.0",
		"9.0.0.0100",
		"9.0.1.0",
		"9.0.2.0",
		"9.0.2.0100",
		"9.1.0.0",
		"9.1.0.0100",
		"9.1.0.0300",
	})
}

// TestVSphereLetterDoesNotOutrankUpdate guards the specific bug that motivated the
// structured vSphere scheme: a generic "split on dots and letters" parser reads "8.0c"
// as [8 0 3] and sorts it above "8.0U3" as [8 0]. It must sort below.
func TestVSphereLetterDoesNotOutrankUpdate(t *testing.T) {
	for _, tc := range []struct{ lo, hi string }{
		{"8.0c", "8.0U1"},
		{"8.0c", "8.0U3"},
		{"8.0.0c", "8.0U3"},
		{"8.0U2f", "8.0U3"},
	} {
		if Compare(Parse(tc.lo, model.SchemeVSphere), Parse(tc.hi, model.SchemeVSphere)) >= 0 {
			t.Errorf("expected %q < %q", tc.lo, tc.hi)
		}
	}
}

// ESX writes "8.0c" where vCenter writes "8.0.0c" for the analogous release. Both must
// normalise to the same key, or cross-product reasoning silently skews.
func TestESXAndVCenterLetterFormsAgree(t *testing.T) {
	esx := Parse("8.0c", model.SchemeVSphere)
	vc := Parse("8.0.0c", model.SchemeVSphere)
	if Compare(esx, vc) != 0 {
		t.Errorf("expected ESX 8.0c == vCenter 8.0.0c, got keys %v vs %v", esx.Key, vc.Key)
	}
}

func TestGenericOrdering(t *testing.T) {
	assertAscending(t, model.SchemeGeneric, []string{
		"1.20.2",
		"1.33.6",
		"1.34.1",
		"1.34.8",
		"1.35.0",
		"1.35.2",
		"1.36.1",
	})
	assertAscending(t, model.SchemeGeneric, []string{
		"3.1.0",
		"3.4.2+v1.33",
		"3.5.0+v1.34",
		"3.5.1+v1.34",
		"3.6.0+v1.35",
		"3.6.3+1.35",
		"3.7.0+v1.36",
	})
	// Supervisor: k8s version dominates, then the vmware build, then the vsc line.
	// The two concurrent vsc families must both order sensibly under the same k8s minor.
	assertAscending(t, model.SchemeGeneric, []string{
		"v1.32.9+vmware.2-fips-vsc0.1.14",
		"v1.32.9+vmware.2-fips-vsc0.1.15",
		"v1.32.9+vmware.2-fips-vsc9.0.2.0",
		"v1.32.9+vmware.2-fips-vsc9.0.2.0100",
		"v1.32.9+vmware.2-fips-vsc9.1.0.0",
		"v1.32.9+vmware.2-fips-vsc9.1.0.0200",
		"v1.33.9+vmware.3-fips-vsc0.1.15",
	})
}

// Old VKr entries carry trailing prose. The leading run must still dominate.
func TestGenericIgnoresTrailingProse(t *testing.T) {
	plain := Parse("1.20.2", model.SchemeGeneric)
	prose := Parse("1.20.2 (TKr 1.20.2 for vSphere 7.x)", model.SchemeGeneric)
	if Compare(plain, prose) >= 0 {
		t.Errorf("expected the bare version to sort at or below the annotated one")
	}
	newer := Parse("1.33.6", model.SchemeGeneric)
	if Compare(prose, newer) >= 0 {
		t.Errorf("expected annotated 1.20.2 < 1.33.6, keys %v vs %v", prose.Key, newer.Key)
	}
}

func TestK8sMinor(t *testing.T) {
	sup, _ := model.ByKey("supervisor")
	vks, _ := model.ByKey("vks")
	vkr, _ := model.ByKey("vkr")
	vc, _ := model.ByKey("vcenter")

	for _, tc := range []struct {
		name    string
		version string
		product model.Product
		want    int
		wantOK  bool
	}{
		{"supervisor", "v1.32.9+vmware.2-fips-vsc9.1.0.0200", sup, 32, true},
		{"vkr", "1.36.1", vkr, 36, true},
		{"vks with tail", "3.7.0+v1.36", vks, 36, true},
		{"vks without tail", "3.1.0", vks, 0, false},
		{"vcenter has none", "8.0U3k", vc, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := K8sMinor(Parse(tc.version, tc.product.Scheme), tc.product)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("K8sMinor(%q) = (%d, %v), want (%d, %v)", tc.version, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestLine(t *testing.T) {
	vks, _ := model.ByKey("vks")
	vc, _ := model.ByKey("vcenter")

	// Every patch of the same k8s minor collapses to one line, which is what keeps
	// upgrade-hop pruning effective.
	a := Line(Parse("3.6.0+v1.35", model.SchemeGeneric), vks)
	b := Line(Parse("3.6.3+1.35", model.SchemeGeneric), vks)
	if a != b {
		t.Errorf("expected same VKS line for the same k8s minor, got %q and %q", a, b)
	}

	same := Line(Parse("8.0U3", model.SchemeVSphere), vc)
	alsoSame := Line(Parse("8.0U3k", model.SchemeVSphere), vc)
	if same != alsoSame {
		t.Errorf("expected 8.0U3 and 8.0U3k on one line, got %q and %q", same, alsoSame)
	}
	diff := Line(Parse("8.0U2", model.SchemeVSphere), vc)
	if diff == same {
		t.Errorf("expected 8.0U2 on a different line from 8.0U3")
	}
}

func TestParsedFlag(t *testing.T) {
	if !Parse("8.0U3k", model.SchemeVSphere).Parsed {
		t.Error("expected 8.0U3k to parse")
	}
	if !Parse("9.1.0.0300", model.SchemeVSphere).Parsed {
		t.Error("expected 9.1.0.0300 to parse")
	}
	// Unknown forms must not panic and must still yield a usable key.
	v := Parse("some-weird-build", model.SchemeVSphere)
	if v.Parsed {
		t.Error("expected an unrecognised vSphere version to report Parsed=false")
	}
	if len(v.Key) == 0 {
		t.Error("expected a fallback key even for unparsed input")
	}
}
