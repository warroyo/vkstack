package graph

import (
	"container/heap"
	"fmt"
	"maps"
	"sort"

	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/version"
)

// Rules constrain what counts as a single upgrade step. Defaults live in DefaultRules;
// they are data rather than code so they can be corrected without a rebuild.
type Rules struct {
	// K8sMinorMaxSkip caps how far the Kubernetes minor may advance in one step, keyed
	// by product short key. Absent means unconstrained.
	K8sMinorMaxSkip map[string]int `json:"k8sMinorMaxSkip"`
}

// DefaultRules encodes the constraints that actually bite in practice.
//
// Kubernetes components cannot skip a minor version, so 1.31 -> 1.33 must pass through
// 1.32. This is the rule the planner mainly exists to enforce; ESX and vCenter have no
// equivalent limit and may jump forward freely.
func DefaultRules() Rules {
	return Rules{
		K8sMinorMaxSkip: map[string]int{
			"supervisor": 1,
			"vks":        1,
			"vkr":        1,
		},
	}
}

// Step is one move in an upgrade plan: advance a single product.
type Step struct {
	Product model.Product
	From    *Release
	To      *Release
	// State is the whole stack after this step, so each step can be shown with the
	// configuration it leaves behind.
	State map[int]*Release
	// Supported reports whether the resulting stack is a compatible steady state.
	//
	// False means the stack is transitional: it is on the way somewhere but is not a
	// configuration to sit on. Consecutive unsupported steps have to happen inside one
	// maintenance window, which is exactly the thing that is hard to work out by hand.
	Supported bool
	// Blocking names the pair that makes an unsupported state unsupported.
	Blocking string
	// Unverified lists pairs in the resulting state that could not be checked.
	Unverified []PairVerdict
}

// Window groups consecutive steps that cannot be left half-done.
type Window struct {
	Steps []Step
	// Transitional is true when the stack is unsupported partway through, so every
	// step in the window must complete before stopping.
	Transitional bool
}

// Windows groups a plan's steps into maintenance windows: each run of steps that leaves
// the stack unsupported, plus the step that returns it to a supported state, must be
// done together.
func (p *Plan) Windows() []Window {
	var out []Window
	var cur Window
	for _, s := range p.Steps {
		cur.Steps = append(cur.Steps, s)
		if !s.Supported {
			cur.Transitional = true
			continue
		}
		out = append(out, cur)
		cur = Window{}
	}
	if len(cur.Steps) > 0 {
		out = append(out, cur)
	}
	return out
}

// Plan is an ordered sequence of single-product upgrades.
type Plan struct {
	Steps []Step
	// Explored is how many states the search visited, for --explain.
	Explored int
}

// PlanOptions control the search.
type PlanOptions struct {
	Rules Rules
	// AllHops disables candidate pruning, considering every forward release rather
	// than the newest of each permitted next line.
	AllHops bool
	// IncludePatches allows patch releases as intermediate steps.
	IncludePatches bool
	// MaxStates caps the search. Zero uses a sane default.
	MaxStates int
	// Explain collects per-candidate accept/reject reasons.
	Explain bool
}

// Rejection records why a candidate hop was not usable, for --explain.
type Rejection struct {
	Product model.Product
	From    *Release
	To      *Release
	Reason  string
}

// PlanFailure describes a search that found no path.
type PlanFailure struct {
	// Reached is the furthest state the search got to.
	Reached map[int]*Release
	// Explored is how many states were visited.
	Explored int
	// HitLimit is true when the search was cut off rather than exhausted.
	HitLimit bool
	// Rejections is populated when Explain is set.
	Rejections []Rejection
}

const defaultMaxStates = 200000

// Hops returns the releases a product can move to from a given release in one step.
//
// Upstream's /upgrades endpoint is not trusted, so this is derived: forward-only, then
// filtered by the per-product step rules. By default the candidates are pruned to the
// newest release of each permitted next line, because offering every patch makes the
// search branch uselessly wide without changing which stacks are reachable.
func (g *Graph) Hops(from *Release, opts PlanOptions) []*Release {
	p, ok := model.ByID(from.ProductID)
	if !ok {
		return nil
	}
	maxSkip, capped := opts.Rules.K8sMinorMaxSkip[p.Key]
	fromMinor, hasMinor := version.K8sMinor(from.Version, p)

	var forward []*Release
	for _, r := range g.ReleasesOf(from.ProductID) {
		if version.Compare(r.Version, from.Version) <= 0 {
			continue
		}
		if r.IsPatch() && !opts.IncludePatches {
			continue
		}
		if capped && hasMinor {
			if toMinor, ok := version.K8sMinor(r.Version, p); ok && toMinor-fromMinor > maxSkip {
				continue
			}
		}
		forward = append(forward, r)
	}
	if opts.AllHops {
		return forward
	}

	// Keep only the newest release of each line.
	newest := map[string]*Release{}
	for _, r := range forward {
		line := version.Line(r.Version, p)
		if cur, ok := newest[line]; !ok || version.Compare(r.Version, cur.Version) > 0 {
			newest[line] = r
		}
	}
	pruned := make([]*Release, 0, len(newest))
	for _, r := range newest {
		pruned = append(pruned, r)
	}
	sort.Slice(pruned, func(i, j int) bool {
		return version.Compare(pruned[i].Version, pruned[j].Version) < 0
	})
	return pruned
}

// legal reports whether a whole state is valid: every published pair compatible.
// Unpublished pairs cannot constrain anything and are skipped, then surfaced per step.
func (g *Graph) legal(state map[int]*Release) bool {
	for _, v := range g.Check(state).Pairs {
		if !v.Unverified() && !Compatible(v.Status) {
			return false
		}
	}
	return true
}

// unsupportedPenalty biases the search towards staying on supported configurations.
// It is a cost, not a prohibition: a stack that is transiently unsupported is normal
// mid-upgrade, but a plan that avoids it is strictly better.
const unsupportedPenalty = 2

// Upgrade plans an ordered sequence of single-product upgrades from current to target.
//
// A* over whole-stack states: a move advances exactly one product along one candidate
// hop. Intermediate states are allowed to be unsupported — they have to be, since
// vCenter cannot move without transiently breaking its pairing with everything it
// manages — but they cost extra, and the *final* state must be a supported steady
// state. Runs of unsupported states are then grouped into maintenance windows.
//
// The heuristic is the number of products not yet at their target, which is admissible
// because each still needs at least one move.
//
// target may name a subset of products; unnamed products are free to move or stay, and
// the search stops as soon as every named product has arrived at a supported state.
func (g *Graph) Upgrade(current, target map[int]*Release, opts PlanOptions) (*Plan, *PlanFailure) {
	if opts.MaxStates <= 0 {
		opts.MaxStates = defaultMaxStates
	}
	if opts.Rules.K8sMinorMaxSkip == nil {
		opts.Rules = DefaultRules()
	}

	start := maps.Clone(current)
	startKey := stateKey(start)

	type node struct {
		state map[int]*Release
		cost  int
	}
	nodes := map[string]node{startKey: {state: start}}
	cameFrom := map[string]struct {
		prev string
		step Step
	}{}

	remaining := func(s map[int]*Release) int {
		n := 0
		for pid, want := range target {
			if got, ok := s[pid]; !ok || got.ID != want.ID {
				n++
			}
		}
		return n
	}

	open := &stateQueue{}
	heap.Init(open)
	seq := 0
	push := func(key string, priority int) {
		seq++
		heap.Push(open, &queued{key: key, priority: priority, seq: seq})
	}
	push(startKey, remaining(start))

	var failure = &PlanFailure{Reached: start}
	best := remaining(start)
	visited := map[string]bool{}

	for open.Len() > 0 {
		if len(visited) >= opts.MaxStates {
			failure.HitLimit = true
			break
		}
		cur := heap.Pop(open).(*queued)
		if visited[cur.key] {
			continue
		}
		visited[cur.key] = true
		curNode := nodes[cur.key]

		left := remaining(curNode.state)
		// The goal is reached only at a supported steady state — arriving at the right
		// versions in a broken configuration is not a finished upgrade.
		if left == 0 && g.legal(curNode.state) {
			return g.buildPlan(cur.key, cameFrom, len(visited)), nil
		}
		// Track the closest state reached, so a failure can say how far it got.
		if left < best {
			best, failure.Reached = left, curNode.state
		}

		// Iterate products in upgrade order, not map order: map iteration is random,
		// which would make plans differ run to run, and expanding in the natural
		// order makes tied-cost plans come out in the order people expect.
		for _, key := range model.UpgradeOrder() {
			p, _ := model.ByKey(key)
			pid := p.ID
			from, present := curNode.state[pid]
			if !present {
				continue
			}
			// Never move a product that is already where the caller asked for it.
			if want, pinned := target[pid]; pinned && from.ID == want.ID {
				continue
			}
			for _, to := range g.Hops(from, opts) {
				next := maps.Clone(curNode.state)
				next[pid] = to

				nk := stateKey(next)
				if visited[nk] {
					continue
				}
				cost := curNode.cost + 1
				supported := g.legal(next)
				if !supported {
					cost += unsupportedPenalty
					if opts.Explain {
						failure.Rejections = append(failure.Rejections, Rejection{
							Product: p, From: from, To: to,
							Reason: g.blockReason(next, pid),
						})
					}
				}
				if existing, ok := nodes[nk]; ok && existing.cost <= cost {
					continue
				}
				nodes[nk] = node{state: next, cost: cost}
				cameFrom[nk] = struct {
					prev string
					step Step
				}{cur.key, Step{
					Product: p, From: from, To: to, State: next,
					Supported: supported,
					Blocking:  blockingIf(!supported, g, next, pid),
				}}
				push(nk, cost+remaining(next))
			}
		}
	}

	failure.Explored = len(visited)
	return nil, failure
}

// blockingIf returns the blocking pair only when the state is actually unsupported,
// so the (non-trivial) Check is skipped on the common supported path.
func blockingIf(unsupported bool, g *Graph, state map[int]*Release, changed int) string {
	if !unsupported {
		return ""
	}
	return g.blockReason(state, changed)
}

// blockReason names the pair that makes a state illegal, for --explain.
func (g *Graph) blockReason(state map[int]*Release, changed int) string {
	for _, v := range g.Check(state).Pairs {
		if v.Unverified() || Compatible(v.Status) {
			continue
		}
		if v.A.ID == changed || v.B.ID == changed {
			return fmt.Sprintf("%s %s × %s %s is not compatible",
				v.A.Label, v.ARelease.Raw, v.B.Label, v.BRelease.Raw)
		}
	}
	return "resulting stack is not compatible"
}

func (g *Graph) buildPlan(goal string, cameFrom map[string]struct {
	prev string
	step Step
}, explored int) *Plan {
	var steps []Step
	for k := goal; ; {
		link, ok := cameFrom[k]
		if !ok {
			break
		}
		steps = append(steps, link.step)
		k = link.prev
	}
	// Walked backwards from the goal; reverse into execution order.
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	// Record which pairs each intermediate state could not verify, so a plan can be
	// read with its blind spots visible rather than looking fully checked.
	for i := range steps {
		steps[i].Unverified = g.Check(steps[i].State).Unverified()
	}
	return &Plan{Steps: steps, Explored: explored}
}

// stateKey renders a state as a stable string for the visited set.
func stateKey(s map[int]*Release) string {
	ids := make([]int, 0, len(s))
	for pid := range s {
		ids = append(ids, pid)
	}
	sort.Ints(ids)
	key := make([]byte, 0, len(ids)*12)
	for _, pid := range ids {
		key = fmt.Appendf(key, "%d:%d;", pid, s[pid].ID)
	}
	return string(key)
}

// queued is one entry in the A* frontier.
type queued struct {
	key      string
	priority int
	// seq is the insertion order, used to break priority ties deterministically so
	// the same inputs always produce the same plan.
	seq   int
	index int
}

type stateQueue []*queued

func (q stateQueue) Len() int { return len(q) }
func (q stateQueue) Less(i, j int) bool {
	if q[i].priority != q[j].priority {
		return q[i].priority < q[j].priority
	}
	return q[i].seq < q[j].seq
}
func (q stateQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i]; q[i].index, q[j].index = i, j }
func (q *stateQueue) Push(x any)   { *q = append(*q, x.(*queued)) }
func (q *stateQueue) Pop() any {
	old := *q
	n := len(old)
	item := old[n-1]
	*q = old[:n-1]
	return item
}
