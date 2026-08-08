package graph

import (
	"testing"

	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/store"
)

// k8sFixture gives Supervisor, VKS and VKr a run of consecutive Kubernetes minors so
// the no-skip rule has something to bite on, and keeps the base layer permissive so it
// never obscures a Kubernetes-side failure.
func k8sFixture() *store.Snapshot {
	snap := &store.Snapshot{}
	add := func(id, product int, hybrid string) {
		snap.Releases = append(snap.Releases, store.Release{
			ID: id, ProductID: product, HybridVersion: hybrid, ReleaseType: "Minor", GADate: 1,
		})
	}
	add(21, vc, "9.0.0.0")
	add(11, esx, "9.0.0.0")
	// Supervisor across k8s 1.30 -> 1.33.
	add(130, sup, "v1.30.0+vmware.1")
	add(131, sup, "v1.31.0+vmware.1")
	add(132, sup, "v1.32.0+vmware.1")
	add(133, sup, "v1.33.0+vmware.1")
	add(40, vks, "3.0.0+v1.30")
	add(50, vkr, "1.30.0")

	for _, pr := range model.Pairs() {
		count := 1
		if isUnpublished(pr) {
			count = 0
		}
		snap.Coverage = append(snap.Coverage, store.PairCoverage{
			AProduct: pr[0], BProduct: pr[1], EdgeCount: count,
		})
	}

	// Everything base-layer is compatible with everything, so only the k8s skip rule
	// and the explicit incompatibilities below can constrain a plan.
	ok := func(a, b int) {
		snap.Compat = append(snap.Compat, store.Compat{ARelease: a, BRelease: b, Status: store.StatusCompatible})
	}
	ok(11, 21)
	for _, s := range []int{130, 131, 132, 133} {
		ok(21, s)
		ok(11, s)
		ok(s, 40)
	}
	ok(21, 40)
	ok(21, 50)
	ok(40, 50)
	return snap
}

func loadK8s(t *testing.T) *Graph {
	t.Helper()
	g, err := Load(k8sFixture(), Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return g
}

// The rule the planner mainly exists to enforce: no Kubernetes minor may advance by
// more than one in a single step.
func TestPlanNeverSkipsAK8sMinor(t *testing.T) {
	g := loadK8s(t)
	current := map[int]*Release{
		vc: g.Releases[21], esx: g.Releases[11], sup: g.Releases[130],
		vks: g.Releases[40], vkr: g.Releases[50],
	}
	target := map[int]*Release{sup: g.Releases[133]}

	plan, failure := g.Upgrade(current, target, PlanOptions{})
	if plan == nil {
		t.Fatalf("expected a plan, got failure %+v", failure)
	}
	if len(plan.Steps) != 3 {
		t.Errorf("expected 3 steps (1.30->1.31->1.32->1.33), got %d", len(plan.Steps))
	}

	supProduct, _ := model.ByKey("supervisor")
	for i, s := range plan.Steps {
		if s.Product.Key != "supervisor" {
			continue
		}
		from, okA := k8sMinorOf(s.From, supProduct)
		to, okB := k8sMinorOf(s.To, supProduct)
		if okA && okB && to-from > 1 {
			t.Errorf("step %d skips from k8s 1.%d to 1.%d", i+1, from, to)
		}
	}
}

func TestHopsRespectsSkipRuleAndPruning(t *testing.T) {
	g := loadK8s(t)
	from := g.Releases[130] // Supervisor k8s 1.30

	hops := g.Hops(from, PlanOptions{Rules: DefaultRules()})
	if len(hops) != 1 || hops[0].ID != 131 {
		got := make([]string, 0, len(hops))
		for _, h := range hops {
			got = append(got, h.Raw)
		}
		t.Errorf("expected only the next k8s minor as a hop, got %v", got)
	}

	// Without the rule, every forward release becomes a candidate.
	unruled := g.Hops(from, PlanOptions{Rules: Rules{K8sMinorMaxSkip: map[string]int{}}})
	if len(unruled) != 3 {
		t.Errorf("expected 3 forward hops with no skip rule, got %d", len(unruled))
	}
}

// Every step's recorded state must match its Supported flag, and the plan must end on
// a supported steady state — arriving at the right versions in a broken configuration
// is not a finished upgrade.
func TestPlanEndsSupportedAndLabelsTransitions(t *testing.T) {
	g := loadK8s(t)
	current := map[int]*Release{
		vc: g.Releases[21], esx: g.Releases[11], sup: g.Releases[130],
		vks: g.Releases[40], vkr: g.Releases[50],
	}
	plan, failure := g.Upgrade(current, map[int]*Release{sup: g.Releases[132]}, PlanOptions{})
	if plan == nil {
		t.Fatalf("expected a plan, got %+v", failure)
	}
	for i, s := range plan.Steps {
		if got := g.Check(s.State).OK(); got != s.Supported {
			t.Errorf("step %d: Supported=%v but Check says %v", i+1, s.Supported, got)
		}
	}
	last := plan.Steps[len(plan.Steps)-1]
	if !last.Supported {
		t.Error("a plan must end on a supported stack")
	}
	if !g.Check(last.State).OK() {
		t.Error("the final state must pass Check")
	}
}

func TestWindowsGroupTransitionalRuns(t *testing.T) {
	p := &Plan{Steps: []Step{
		{Supported: false}, {Supported: false}, {Supported: true}, // one window
		{Supported: true}, // another
	}}
	w := p.Windows()
	if len(w) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(w))
	}
	if len(w[0].Steps) != 3 || !w[0].Transitional {
		t.Errorf("first window should hold 3 steps and be transitional, got %d steps transitional=%v",
			len(w[0].Steps), w[0].Transitional)
	}
	if len(w[1].Steps) != 1 || w[1].Transitional {
		t.Errorf("second window should hold 1 non-transitional step")
	}
}

// An unreachable target must report a failure with how far it got, rather than hanging
// or returning a bogus plan.
func TestPlanReportsUnreachableTarget(t *testing.T) {
	g := loadK8s(t)
	current := map[int]*Release{
		vc: g.Releases[21], esx: g.Releases[11], sup: g.Releases[133],
		vks: g.Releases[40], vkr: g.Releases[50],
	}
	// Ask to go backwards; moves are forward-only, so this cannot succeed.
	plan, failure := g.Upgrade(current, map[int]*Release{sup: g.Releases[130]}, PlanOptions{})
	if plan != nil {
		t.Fatal("expected no plan when the target is behind the current version")
	}
	if failure == nil || len(failure.Reached) == 0 {
		t.Fatalf("expected a failure reporting the furthest state reached, got %+v", failure)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	g := loadK8s(t)
	current := map[int]*Release{
		vc: g.Releases[21], esx: g.Releases[11], sup: g.Releases[130],
		vks: g.Releases[40], vkr: g.Releases[50],
	}
	target := map[int]*Release{sup: g.Releases[133]}

	first, _ := g.Upgrade(current, target, PlanOptions{})
	for range 5 {
		next, _ := g.Upgrade(current, target, PlanOptions{})
		if len(first.Steps) != len(next.Steps) {
			t.Fatalf("plan length varies between runs: %d vs %d", len(first.Steps), len(next.Steps))
		}
		for i := range first.Steps {
			if first.Steps[i].To.ID != next.Steps[i].To.ID {
				t.Fatalf("step %d differs between runs", i+1)
			}
		}
	}
}

// k8sMinorOf is a small test helper mirroring what the planner uses internally.
func k8sMinorOf(r *Release, p model.Product) (int, bool) {
	if p.K8sMinorRun < 0 || p.K8sMinorRun >= len(r.Version.Key) {
		return 0, false
	}
	run := r.Version.Key[p.K8sMinorRun]
	if len(run) < 2 {
		return 0, false
	}
	return run[1], true
}
