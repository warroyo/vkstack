package graph

import (
	"container/heap"
	"fmt"
	"regexp"
	"strconv"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/version"
)

// Path answers the question the matrix cannot answer on its own: how to get from one
// stack to another so that every state in between is also a valid stack.
//
// A plan built by hand from single lookups gets this wrong in predictable ways. It moves
// vCenter to a patch that no longer takes the running Supervisor, or it moves the
// Supervisor past the last build an optional product accepts. And it rarely knows whether
// a shorter route existed. This searches the space instead. Each step changes exactly one
// product, and every state on the way is checked on every enforced pair among the products
// being planned, using the same reading as the solver: inside a published pair, a missing
// cell is a no.
//
// The moves follow how these products are actually upgraded:
//
//   - Every product moves forward only, and never past its target.
//   - The Supervisor and VKr move at most one Kubernetes minor per step. A patch or a
//     release-train switch at the same minor is also one step.
//   - The Supervisor never moves from the vsc9 train back to vsc0.
//
// Guest clusters (VKr) are constrained by VKS alone, the pair upstream publishes. VKr is
// provisioned by VKS, and nothing relates it to the Supervisor's own Kubernetes version.
//
// One published "no" is tolerated on purpose: ESX 8 against a vsc9 Supervisor. Upstream
// lists no such pairs, but going from vSphere 8 to 9, Broadcom's documented upgrade order
// is vCenter, VKS, Supervisor, then ESX. So the Supervisor reaches the vsc9 train while the
// hosts are still on 8, and that state has to exist. The step is flagged Transitional, and
// the search keeps such states to the fewest it can.
//
// The matrix says nothing about whether a hop is a supported upgrade: back-in-time rules,
// an optional product's upgrade checklist, known issues. A path is a route through valid
// states, not a guarantee that each hop is allowed. PathOptions.Exclude is how a caller
// removes releases it knows cannot be upgraded to.

// PathOptions tunes the search.
type PathOptions struct {
	// Exclude holds release ids the path must never land on, e.g. a vCenter build a
	// back-in-time rule blocks. Start and target releases are never excluded.
	Exclude map[int]bool
	// MaxStates bounds the search. Zero means a default large enough for any real plan.
	MaxStates int
}

// PathStep is one move: a single product changing release, and the whole stack after it.
type PathStep struct {
	Product model.Product
	From    *Release
	To      *Release
	// State is every planned product's release after this step, keyed by product id.
	State map[int]*Release
	// NotTested lists pairs in State that upstream marks "compatible, not tested".
	NotTested []PairVerdict
	// Transitional marks the ESX 8 with vsc9 Supervisor state, which the matrix does not
	// list but the 8 → 9 upgrade order requires.
	Transitional bool
}

// PathResult is a complete route.
type PathResult struct {
	Products []model.Product
	Start    map[int]*Release
	Target   map[int]*Release
	Steps    []PathStep
	// StartNotTested and StartTransitional describe the starting state itself.
	StartNotTested []PairVerdict
	// Explored is how many states the search visited, for diagnostics.
	Explored int
}

// PathFailure explains why no route exists.
type PathFailure struct {
	// Reason is one of "backwards", "start_invalid", "target_invalid", "no_route",
	// "too_large".
	Reason  string
	Message string
	// Blocking holds the pairs that make the start or target invalid.
	Blocking []PairVerdict
	// BlockedBy names optional products whose removal would make a route exist. Empty
	// when no single optional product is to blame.
	BlockedBy []model.Product
	Explored  int
}

func (f *PathFailure) Error() string { return f.Message }

const (
	pathStepCost         = 1000
	pathTransitionalCost = 400
	pathNotTestedCost    = 2
	pathStaleCost        = 1
	defaultMaxStates     = 2_000_000
)

var trainRe = regexp.MustCompile(`-vsc(\d+)\.`)

// supervisorTrain returns the Supervisor release train number ("0" or "9"), or -1.
func supervisorTrain(r *Release) int {
	if m := trainRe.FindStringSubmatch(r.Raw); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1
}

// Path finds the shortest route from one pinned stack to another. Both maps must pin the
// same products. Optional products take part only when pinned.
func (g *Graph) Path(from, to map[int]*Release, opts PathOptions) (*PathResult, *PathFailure) {
	var prods []model.Product
	for _, p := range model.Products {
		_, a := from[p.ID]
		_, b := to[p.ID]
		if a != b {
			return nil, &PathFailure{Reason: "usage",
				Message: fmt.Sprintf("%s is pinned on one side only; pin it in both the start and the target", p.Label)}
		}
		if a {
			prods = append(prods, p)
		}
	}
	if len(prods) == 0 {
		return nil, &PathFailure{Reason: "usage", Message: "pin at least one product in both the start and the target"}
	}
	for _, p := range prods {
		if version.Compare(from[p.ID].Version, to[p.ID].Version) > 0 {
			return nil, &PathFailure{Reason: "backwards",
				Message: fmt.Sprintf("%s target %s is older than the start %s; paths only move forward",
					p.Label, to[p.ID].Raw, from[p.ID].Raw)}
		}
	}

	res, fail := g.searchPath(prods, from, to, opts)
	if fail == nil || fail.Reason != "no_route" {
		return res, fail
	}
	// Name the optional product in the way, if dropping one would open a route. Optional
	// products have the narrowest windows, and that is almost always the answer.
	for _, p := range prods {
		if !p.Optional || len(prods) < 3 {
			continue
		}
		var rest []model.Product
		f2, t2 := map[int]*Release{}, map[int]*Release{}
		for _, q := range prods {
			if q.ID != p.ID {
				rest = append(rest, q)
				f2[q.ID], t2[q.ID] = from[q.ID], to[q.ID]
			}
		}
		if _, f := g.searchPath(rest, f2, t2, opts); f == nil {
			fail.BlockedBy = append(fail.BlockedBy, p)
		}
	}
	return nil, fail
}

// pathState is one release id per planned product, in plan order.
type pathState [8]int

type pathItem struct {
	cost  int
	state pathState
	index int
}

type pathQueue []*pathItem

func (q pathQueue) Len() int           { return len(q) }
func (q pathQueue) Less(i, j int) bool { return q[i].cost < q[j].cost }
func (q pathQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i]; q[i].index = i; q[j].index = j }
func (q *pathQueue) Push(x any)        { it := x.(*pathItem); it.index = len(*q); *q = append(*q, it) }
func (q *pathQueue) Pop() any          { old := *q; n := len(old); it := old[n-1]; *q = old[:n-1]; return it }

func (g *Graph) searchPath(prods []model.Product, from, to map[int]*Release, opts PathOptions) (*PathResult, *PathFailure) {
	n := len(prods)
	maxStates := opts.MaxStates
	if maxStates == 0 {
		maxStates = defaultMaxStates
	}

	// Candidate releases per product: forward of the start, not past the target, and
	// not excluded. Ascending by version, so index order is upgrade order.
	cands := make([][]*Release, n)
	idx := make([]map[int]int, n)
	for i, p := range prods {
		lo, hi := from[p.ID], to[p.ID]
		idx[i] = map[int]int{}
		for _, r := range g.ReleasesOf(p.ID) {
			if version.Compare(r.Version, lo.Version) < 0 || version.Compare(r.Version, hi.Version) > 0 {
				continue
			}
			if opts.Exclude[r.ID] && r.ID != lo.ID && r.ID != hi.ID {
				continue
			}
			idx[i][r.ID] = len(cands[i])
			cands[i] = append(cands[i], r)
		}
	}

	// Status lookups, indexed once: the search asks the same pairs many times.
	statusOf := map[[2]int]int{}
	for i := range prods {
		for _, r := range cands[i] {
			for _, e := range g.Compat[r.ID] {
				statusOf[[2]int{r.ID, e.Peer}] = e.Status
			}
		}
	}
	type pairIdx struct{ a, b int }
	var enforced []pairIdx
	for i := range prods {
		for j := i + 1; j < n; j++ {
			if model.Constrains(prods[i].ID, prods[j].ID) && g.Published(prods[i].ID, prods[j].ID) {
				enforced = append(enforced, pairIdx{i, j})
			}
		}
	}

	minorOf := func(i int, r *Release) (int, bool) { return version.K8sMinor(r.Version, prods[i]) }

	rel := func(s pathState, i int) *Release { return g.Releases[s[i]] }

	// newestInMinor marks, for the Supervisor and VKr, the newest candidate within each
	// Kubernetes minor. Among equally short routes the search prefers landing on those,
	// so a tie is not broken by an arbitrary older patch.
	stale := map[int]bool{}
	for i, p := range prods {
		if p.Key != "supervisor" && p.Key != "vkr" {
			continue
		}
		newest := map[int]int{}
		for _, r := range cands[i] {
			if m, ok := minorOf(i, r); ok {
				newest[m] = r.ID // ascending, so the last one wins
			}
		}
		for _, r := range cands[i] {
			if m, ok := minorOf(i, r); ok && newest[m] != r.ID {
				stale[r.ID] = true
			}
		}
	}

	// evaluate returns whether a state is valid, whether it is transitional, and its
	// not-tested pair count.
	evaluate := func(s pathState) (ok, transitional bool, notTested int) {
		for _, pr := range enforced {
			ra, rb := rel(s, pr.a), rel(s, pr.b)
			st, has := statusOf[[2]int{ra.ID, rb.ID}]
			if !has {
				st, has = statusOf[[2]int{rb.ID, ra.ID}]
			}
			if has && Compatible(st) {
				if st == 3 {
					notTested++
				}
				continue
			}
			if isTransitionalPair(prods[pr.a], ra, prods[pr.b], rb) {
				transitional = true
				continue
			}
			return false, false, 0
		}
		return true, transitional, notTested
	}

	var start, goal pathState
	for i, p := range prods {
		start[i], goal[i] = from[p.ID].ID, to[p.ID].ID
	}
	if ok, _, _ := evaluate(start); !ok {
		return nil, &PathFailure{Reason: "start_invalid",
			Message:  "the starting stack is not valid in the matrix",
			Blocking: g.blockingFor(prods, start)}
	}
	if ok, _, _ := evaluate(goal); !ok {
		return nil, &PathFailure{Reason: "target_invalid",
			Message:  "the target stack is not valid in the matrix",
			Blocking: g.blockingFor(prods, goal)}
	}

	dist := map[pathState]int{start: 0}
	prev := map[pathState]pathState{}
	pq := &pathQueue{{cost: 0, state: start}}
	done := map[pathState]bool{}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(*pathItem)
		if done[cur.state] {
			continue
		}
		done[cur.state] = true
		if cur.state == goal {
			return g.buildPath(prods, from, to, start, goal, prev, len(done), evaluate), nil
		}
		if len(done) > maxStates {
			return nil, &PathFailure{Reason: "too_large", Explored: len(done),
				Message: "the search space is too large; pin fewer products or narrow the target"}
		}
		for i, p := range prods {
			curRel := rel(cur.state, i)
			ci := idx[i][curRel.ID]
			for _, next := range cands[i][ci+1:] {
				if p.Key == "supervisor" || p.Key == "vkr" {
					cm, ok1 := minorOf(i, curRel)
					nm, ok2 := minorOf(i, next)
					if ok1 && ok2 && nm-cm > 1 {
						break // ascending: every later release is further still
					}
				}
				if p.Key == "supervisor" && supervisorTrain(curRel) == 9 && supervisorTrain(next) == 0 {
					continue
				}
				ns := cur.state
				ns[i] = next.ID
				if done[ns] {
					continue
				}
				ok, tr, nt := evaluate(ns)
				if !ok {
					continue
				}
				c := cur.cost + pathStepCost + nt*pathNotTestedCost
				if stale[next.ID] {
					c += pathStaleCost
				}
				if tr {
					c += pathTransitionalCost
				}
				if d, seen := dist[ns]; !seen || c < d {
					dist[ns], prev[ns] = c, cur.state
					heap.Push(pq, &pathItem{cost: c, state: ns})
				}
			}
		}
	}
	return nil, &PathFailure{Reason: "no_route", Explored: len(done),
		Message: "no sequence of single-product upgrades keeps every state valid"}
}

// isTransitionalPair reports the one tolerated published "no": ESX 8 against a vsc9
// Supervisor, the state the vSphere 8 → 9 upgrade order passes through.
func isTransitionalPair(pa model.Product, ra *Release, pb model.Product, rb *Release) bool {
	if pa.Key == "supervisor" {
		pa, ra, pb, rb = pb, rb, pa, ra
	}
	return pa.Key == "esx" && pb.Key == "supervisor" &&
		ra.Version.Major() == 8 && supervisorTrain(rb) == 9
}

func (g *Graph) blockingFor(prods []model.Product, s pathState) []PairVerdict {
	pins := map[int]*Release{}
	for i, p := range prods {
		pins[p.ID] = g.Releases[s[i]]
	}
	return g.Check(pins).Blocking()
}

func (g *Graph) buildPath(prods []model.Product, from, to map[int]*Release, start, goal pathState,
	prev map[pathState]pathState, explored int,
	evaluate func(pathState) (bool, bool, int)) *PathResult {

	var chain []pathState
	for s := goal; ; s = prev[s] {
		chain = append(chain, s)
		if s == start {
			break
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	stateMap := func(s pathState) map[int]*Release {
		m := map[int]*Release{}
		for i, p := range prods {
			m[p.ID] = g.Releases[s[i]]
		}
		return m
	}
	notTested := func(s pathState) []PairVerdict {
		var out []PairVerdict
		for _, v := range g.Check(stateMap(s)).Pairs {
			if v.Constrains && v.HasEdge && v.Status == 3 {
				out = append(out, v)
			}
		}
		return out
	}

	res := &PathResult{Products: prods, Start: from, Target: to, Explored: explored,
		StartNotTested: notTested(start)}
	for k := 1; k < len(chain); k++ {
		a, b := chain[k-1], chain[k]
		for i, p := range prods {
			if a[i] == b[i] {
				continue
			}
			_, tr, _ := evaluate(b)
			res.Steps = append(res.Steps, PathStep{
				Product: p, From: g.Releases[a[i]], To: g.Releases[b[i]],
				State: stateMap(b), NotTested: notTested(b), Transitional: tr,
			})
		}
	}
	return res
}
