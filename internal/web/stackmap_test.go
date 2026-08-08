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

// Several entries under one Supervisor minor are the same minor delivered by different
// vSphere releases, not competing patches. Verified against the live matrix: for a given
// vCenter and a given delivery there is exactly one Kubernetes patch per minor. The node
// text has to say that, because reading the list as a choice between patches is the
// easiest mistake to make here.
func TestSupervisorDetailNamesDeliveriesNotPatches(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.30.14+vmware.8-fips-vsc9.1.0.0"),
		supRelease("v1.30.10+vmware.1-fips-vsc9.0.1.0"),
		supRelease("v1.30.5+vmware.4-fips-vsc9.0.0.0"),
	}

	detail, labels := describeSupervisor(rels, "9.1.0.0")
	if detail != "1.30.14 · ships with this vCenter, 2 older still supported" {
		t.Errorf("detail = %q", detail)
	}
	if len(labels) != 3 || labels[0] != "v1.30.14+vmware.8-fips-vsc9.1.0.0 — ships with vCenter 9.1.0.0" {
		t.Errorf("expected the shipped build first and labelled, got %v", labels)
	}

	// The build that ships with the pin leads even when it is not first in the input.
	detail, labels = describeSupervisor(rels, "9.0.0.0")
	if detail != "1.30.5 · ships with this vCenter, 2 older still supported" {
		t.Errorf("detail with an older pin = %q", detail)
	}
	if labels[0] != "v1.30.5+vmware.4-fips-vsc9.0.0.0 — ships with vCenter 9.0.0.0" {
		t.Errorf("expected the matching delivery promoted, got %q", labels[0])
	}
}

// Two async deliveries of the identical Kubernetes patch must not read as two patches.
func TestSupervisorDetailCollapsesOneK8sPatch(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.15"),
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.14"),
	}
	detail, _ := describeSupervisor(rels, "8.0U3k")
	if detail != "1.32.9 · in 2 releases" {
		t.Errorf("detail = %q, want the single patch named once", detail)
	}
}
