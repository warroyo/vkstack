package web

import (
	"testing"

	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/version"
)

func supRelease(raw string) *graph.Release {
	p, _ := model.ByKey("supervisor")
	return &graph.Release{Raw: raw, ProductKey: "supervisor", Version: version.Parse(raw, p.Scheme)}
}

// Supervisor ships the same Kubernetes version on two trains, and they are not
// interchangeable: a vCenter 8 deployment takes vsc0 only. Grouping by Kubernetes minor
// alone merged them into one node that looked compatible with everything.
func TestSupervisorTrainsAreSeparateNodes(t *testing.T) {
	p, _ := model.ByKey("supervisor")

	vsc9 := supRelease("v1.31.11+vmware.8-fips-vsc9.1.0.0")
	vsc0 := supRelease("v1.31.11+vmware.1-fips-vsc0.1.15")

	if got := supervisorTrain(vsc9); got != "vsc9" {
		t.Errorf("train of %q = %q, want vsc9", vsc9.Raw, got)
	}
	if got := supervisorTrain(vsc0); got != "vsc0" {
		t.Errorf("train of %q = %q, want vsc0", vsc0.Raw, got)
	}

	a, b := lineKey(p, vsc9), lineKey(p, vsc0)
	if a == b {
		t.Fatalf("both trains collapsed to one node key %q — the distinction is lost", a)
	}
	if a != "1.31 vsc9" || b != "1.31 vsc0" {
		t.Errorf("line keys = %q and %q, want \"1.31 vsc9\" and \"1.31 vsc0\"", a, b)
	}
}

// VKr carries a Kubernetes minor too but has only one train, so it must not sprout a
// train suffix.
func TestNonSupervisorProductsHaveNoTrain(t *testing.T) {
	vkr, _ := model.ByKey("vkr")
	r := &graph.Release{Raw: "1.36.1", Version: version.Parse("1.36.1", vkr.Scheme)}
	if got := lineKey(vkr, r); got != "1.36" {
		t.Errorf("VKr line key = %q, want \"1.36\"", got)
	}

	vc, _ := model.ByKey("vcenter")
	v := &graph.Release{Raw: "8.0U3k", Version: version.Parse("8.0U3k", vc.Scheme)}
	if got := lineKey(vc, v); got != "8.0U3k" {
		t.Errorf("vCenter must not be collapsed, got %q", got)
	}
}
