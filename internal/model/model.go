// Package model holds the conceptual dependency model: which products exist, how they
// relate, and why. It is the single source of truth behind the mermaid diagram, the
// generated docs/model.md, `vkstack explain`, and the web whiteboard view.
//
// The prose here is authored domain knowledge, not derived from the API. The version
// evidence supports the Supervisor<->vCenter coupling (versions embed the vCenter line,
// e.g. "-fips-vsc9.1.0.0200") and the VKS<->Kubernetes coupling ("3.7.0+v1.36"); the rest
// is reviewable and expected to be corrected in place.
package model

import (
	"slices"
	"strings"
)

// Scheme selects how a product's version strings are parsed and ordered.
type Scheme string

const (
	// SchemeVSphere covers ESX and vCenter: "8.0U3k", "8.0.0c", "9.1.0.0300".
	SchemeVSphere Scheme = "vsphere"
	// SchemeGeneric covers Supervisor, VKS and VKr: semver-ish with build metadata.
	SchemeGeneric Scheme = "generic"
)

// Product is one component of the stack.
type Product struct {
	Key    string // short CLI key: esx, vcenter, supervisor, vks, vkr
	ID     int    // upstream interop product id
	Name   string // upstream product name
	Label  string // short display name for diagrams
	Scheme Scheme
	// Example is a real version string, shown in the diagram so the shape of each
	// product's versioning is visible at a glance.
	Example string
	// MinVersion is the supported-version floor, or "" for no hardcoded floor.
	// Products without a floor are filtered by reachability instead.
	MinVersion string
	// K8sMinorRun is the index of the numeric run holding the Kubernetes minor
	// version, or -1 if the product does not track Kubernetes.
	K8sMinorRun int
	// UpgradeOrder is the position of this product in a stack upgrade, lowest first.
	UpgradeOrder int
	// Trains names the concurrent release trains a product ships, where it ships more
	// than one at the same version. Empty for products with a single line.
	Trains []string
	// TrainNote explains what the trains mean, shown wherever they are.
	TrainNote string
	// Optional marks a component that is genuinely absent from many deployments. A stack
	// that omits one is a complete answer, not a partial one, so the solver leaves it out
	// unless the caller pins it or asks for it by name.
	//
	// Optional products are independent of each other and opted into one at a time; nothing
	// may treat one as implying another. NSX and Avi are the networking pair — all four
	// combinations (neither, either alone, both) are real deployments — and TMC Self-Managed
	// is a management plane installed on top of the guest-cluster layer.
	//
	// What each constrains differs. NSX and Avi are deliberately narrow: enforced against
	// each other, the Supervisor and vCenter, and nothing else — the pairs upstream
	// publishes against ESX are reported but never enforced, because vCenter and ESX move
	// together so the host pair excludes nothing the vCenter pair does not. TMC Self-Managed
	// is the opposite: a real dependency of vCenter, VKS and VKr, all three published and
	// enforced when it is in the stack.
	Optional bool
	// Lifecycle says where this product's published support dates live. A zero value
	// means the lifecycle portal has nothing for it and only the matrix flags apply.
	Lifecycle Lifecycle
}

// Match is how a lifecycle row's release string is joined onto an interop release.
type Match string

const (
	// MatchExact: the two sources use the same string. VKS publishes "3.4.2+v1.33" on
	// both sides.
	MatchExact Match = ""
	// MatchMinor: lifecycle publishes a version line. VKr's "1.32" covers 1.32.0
	// through 1.32.10.
	MatchMinor Match = "minor"
	// MatchPrefix: lifecycle publishes a coarser build. vCenter "8.0U3" covers 8.0U3k,
	// NSX "9.1.0.0" covers 9.1.0.0200.
	MatchPrefix Match = "prefix"
)

// Lifecycle locates a product in the Broadcom Product Lifecycle portal.
//
// Two filters are needed because neither of the portal's own fields identifies a product
// on its own. productName is a portfolio bucket: "VMware vSphere Kubernetes Service"
// omits two real VKS releases that file under "Tanzu Kubernetes Runtime", and the same
// bucket carries Avi and vCenter rows. description names the actual product, but as a
// filter it over-collects — "VMware NSX" pulls in unrelated Network Detection and
// Response builds.
//
// So the query narrows server-side by whichever field is usable, and Keep narrows what
// comes back by description.
type Lifecycle struct {
	// ProductName and Description are exact-match server-side filters. Empty means the
	// field is not filtered on. At least one must be set for a product to be sourced.
	ProductName string
	Description string
	// Keep is a description allow-list applied to the rows that come back. An entry
	// ending in "*" matches by prefix, anything else matches exactly. Empty keeps
	// everything.
	//
	// Both forms are needed. vCenter writes a per-release description ("VMware vCenter
	// Server 8.0U2c") so it can only be matched by prefix, while Avi needs exact
	// matching or "VMware Avi Load Balancer Conversion Tool" comes along with it.
	Keep []string
	// Match is the fallback join used when no lifecycle row carries the release string
	// verbatim.
	Match Match
	// NoTechnicalGuidance marks a product Broadcom publishes no technical guidance
	// period for, so passing end of general support ends support outright.
	NoTechnicalGuidance bool
}

// Sourced reports whether the lifecycle portal has data for this product.
func (l Lifecycle) Sourced() bool { return l.ProductName != "" || l.Description != "" }

// Keeps reports whether a row's description belongs to this product.
func (l Lifecycle) Keeps(description string) bool {
	if len(l.Keep) == 0 {
		return true
	}
	for _, want := range l.Keep {
		if prefix, ok := strings.CutSuffix(want, "*"); ok {
			if strings.HasPrefix(description, prefix) {
				return true
			}
			continue
		}
		if description == want {
			return true
		}
	}
	return false
}

// Products are the eight components in scope, in diagram order.
//
// Five are always part of a stack. Three are Optional: NSX and Avi sit between the
// hypervisor and the Supervisor, and TMC Self-Managed sits on top of the guest-cluster
// layer. Each is opted into individually, and a stack without any of them is still a
// complete answer.
//
// VCF is deliberately absent: it is a wrapper bill-of-materials over these component
// versions, not an independent compatibility axis.
var Products = []Product{
	{
		Key: "vcenter", ID: 2, Name: "VMware vCenter", Label: "vCenter",
		Scheme: SchemeVSphere, Example: "8.0U3k", MinVersion: "8.0U3",
		K8sMinorRun: -1, UpgradeOrder: 1,
		// "VMware vCenter*" rather than "VMware vCenter Server*": the 9.1.0.0 row is
		// filed under "VMware vCenter for vSphere Edge", and it is the only place that
		// build's date is published.
		Lifecycle: Lifecycle{
			ProductName: "VMware vCenter Server",
			Keep:        []string{"VMware vCenter*"},
			Match:       MatchPrefix,
		},
	},
	{
		Key: "esx", ID: 1, Name: "VMware ESX", Label: "ESX",
		Scheme: SchemeVSphere, Example: "8.0U3k", MinVersion: "8.0U3",
		K8sMinorRun: -1, UpgradeOrder: 2,
		// ESX is published thinly and coarsely: the whole 8 line is one row, "8.x".
		// Everything else under this productName is another product riding it —
		// vSphere Replication, Live Recovery, vSAN Data Protection — so the allow-list
		// is doing real work here.
		Lifecycle: Lifecycle{
			ProductName: "VMware vSphere ESXi",
			Keep: []string{
				"VMware vSphere (ESXi)",
				"VMware vSphere Hypervisor",
				"VMware vSphere - *",
				"VMware vSphere Enterprise Plus*",
				"VMware vSphere for vSAN Witness*",
				"VMware Cloud Foundation",
				"VMware vSphere Foundation",
			},
			Match: MatchPrefix,
		},
	},
	{
		Key: "nsx", ID: 912, Name: "VMware NSX", Label: "NSX",
		Scheme: SchemeVSphere, Example: "9.1.0.0200",
		K8sMinorRun: -1, UpgradeOrder: 3, Optional: true,
		// NSX 9.x is published under the Cloud Foundation description rather than its
		// own, and only down to 9.1.0.0 — the 9.1.0.0200 builds join by prefix.
		Lifecycle: Lifecycle{
			ProductName: "VMware NSX",
			Keep:        []string{"VMware NSX", "VMware Cloud Foundation"},
			Match:       MatchPrefix,
		},
	},
	{
		Key: "avi", ID: 1795, Name: "Avi Load Balancer", Label: "Avi",
		Scheme: SchemeGeneric, Example: "32.1.2",
		K8sMinorRun: -1, UpgradeOrder: 4, Optional: true,
		// The older name is kept because the portal still files Avi's 22.1.x and 30.1.x
		// lines under it. Both entries are exact: prefix matching would drag in the
		// conversion tools and the Kubernetes operator, which version independently.
		Lifecycle: Lifecycle{
			ProductName: "VMware Avi Load Balancer",
			Keep:        []string{"VMware Avi Load Balancer", "VMware NSX Advanced Load Balancer"},
		},
	},
	{
		Key: "supervisor", ID: 1378, Name: "VMware vSphere Supervisor", Label: "Supervisor",
		Scheme: SchemeGeneric, Example: "v1.32.9+vmware.2-fips-vsc9.1.0.0200",
		K8sMinorRun: 0, UpgradeOrder: 5,
		// No Lifecycle: the portal has no Supervisor entry under any name. It ships
		// inside vCenter and VCF and is never listed on its own, so the Supervisor is
		// the one product still judged solely by the matrix flags.
		Trains: []string{"vsc9", "vsc0"},
		TrainNote: "vsc9.x ships with vCenter 9.x; vsc0.x is versioned independently. " +
			"The same Kubernetes version exists on both and they are not interchangeable.",
	},
	{
		Key: "vks", ID: 1794, Name: "vSphere Kubernetes Service", Label: "VKS",
		Scheme: SchemeGeneric, Example: "3.7.0+v1.36",
		K8sMinorRun: 1, UpgradeOrder: 6,
		// Queried by description, not by name: the "VMware vSphere Kubernetes Service"
		// bucket is missing 3.4.1+v1.33 and 3.5.1+v1.34, which file under "Tanzu
		// Kubernetes Runtime" instead. The description collects all of them, duplicated
		// across every portfolio bucket that ships VKS.
		Lifecycle: Lifecycle{
			Description:         "vSphere Kubernetes Service",
			NoTechnicalGuidance: true,
		},
	},
	{
		Key: "vkr", ID: 820, Name: "vSphere Kubernetes releases", Label: "VKr",
		Scheme: SchemeGeneric, Example: "1.36.1",
		K8sMinorRun: 0, UpgradeOrder: 7,
		// The one product that has to be queried by name: VKr's description is written
		// per release ("VKr 1.32", "TKr 1.28.15 for vSphere 8.x"), so it cannot be a
		// filter. Rows are per Kubernetes minor, so 1.32 answers for 1.32.0 to 1.32.10.
		Lifecycle: Lifecycle{
			ProductName:         "vSphere Kubernetes releases",
			Match:               MatchMinor,
			NoTechnicalGuidance: true,
		},
	},
	{
		Key: "tmc", ID: 1771, Name: "Tanzu Mission Control Self-Managed", Label: "TMC-SM",
		Scheme: SchemeGeneric, Example: "1.4.5",
		K8sMinorRun: -1, UpgradeOrder: 8, Optional: true,
		// Optional, and the one optional product that is a genuine dependency: it is a
		// management plane installed on top of a guest cluster, versioned against vCenter,
		// VKS and VKr, all three published in the matrix. It has no version floor of its
		// own — reachability through vCenter drops any release that only ever paired with
		// a vSphere line now out of scope.
		//
		// Sourced by description: the portal files it under a handful of casings
		// ("Self-managed", "Self-Managed", and a couple with the version appended), so the
		// Keep prefix collects them all while excluding the Cloud Director extension that
		// shares the "Tanzu Mission Control" name. Releases match verbatim ("1.4.5"), so
		// the exact join needs no fallback. Like VKS and VKr the portal publishes no end of
		// technical guidance, so passing end of general support ends support outright.
		Lifecycle: Lifecycle{
			Description:         "Tanzu Mission Control Self-managed",
			Keep:                []string{"Tanzu Mission Control Self-*"},
			NoTechnicalGuidance: true,
		},
	},
}

// Generations are the vSphere platform generations offered as a filter, newest first,
// named by the vCenter major version.
//
// A generation is a statement about vCenter and nothing else. Every other product spans
// both in the published data — NSX 4.x has more compatible vCenter 9 pairs than NSX 9.x
// does, ESX 8.x pairs with vCenter 9 in the hundreds, and Supervisor's vsc0 train, VKS,
// VKr and Avi all cross the line. Filtering any of them by their own major would delete
// compatibility upstream actually publishes, so the filter constrains vCenter and lets
// everything else fall out by whether it can still reach one.
//
// Fixed here rather than derived from the cache so the static build always writes a known
// number of bundles. A vCenter 10 is a one-line change, and TestGenerationsCoverCache
// fails until it is made.
var Generations = []int{9, 8}

// KnownGeneration reports whether gen names a generation the filter offers. Zero means
// every generation and is always valid.
func KnownGeneration(gen int) bool {
	return gen == 0 || slices.Contains(Generations, gen)
}

// Evidence records how a relationship is known, so the explainer can never present an
// asserted rule and a published fact as though they carry the same weight.
type Evidence string

const (
	// EvidencePublished: the interop matrix carries this pair directly. Highest
	// confidence — every claim is a lookup.
	EvidencePublished Evidence = "published"
	// EvidenceInferred: upstream publishes nothing for this pair. The relationship is
	// real, but any answer has to be derived through products that are published.
	EvidenceInferred Evidence = "inferred"
	// EvidenceDomain: how the products relate operationally. Not encoded in the matrix
	// at all, and not checkable against it — this is asserted knowledge and the part
	// most likely to be wrong.
	EvidenceDomain Evidence = "domain"
)

// Describe returns a short phrase naming where a claim comes from.
func (e Evidence) Describe() string {
	switch e {
	case EvidencePublished:
		return "published in the matrix"
	case EvidenceInferred:
		return "inferred — upstream publishes nothing for this pair"
	case EvidenceDomain:
		return "operational knowledge, not in the matrix"
	}
	return string(e)
}

// Edge is a conceptual relationship between two products.
type Edge struct {
	From  string // product key
	To    string // product key
	Label string // short arrow label for the diagram
	// Summary is the one-line version, for the on-screen explainer. Generic and short:
	// the shape of the relationship, not its details.
	Summary string
	// Prose is the full explanation, for the generated doc and `vkstack explain`.
	Prose string
	// Bidirectional marks a mutual version-pairing constraint rather than a
	// "this one determines that one" direction.
	Bidirectional bool
	// Primary marks the pairs that constrain a solve. It is the constraint set, not a
	// claim about which relationships are real, and the two are not the same question.
	//
	// Most non-primary pairs are not dependencies at all: the matrix publishes vCenter
	// against VKS and against VKr, but VKS runs on the Supervisor, so enforcing those
	// produces combinations that are listed compatible yet cannot exist.
	//
	// ESX × NSX is the case that shows why the flag means enforcement rather than
	// dependency. NSX leans heavily on the hosts it prepares as transport nodes — a real
	// dependency by any reading — but its published grid is nearly as permissive as the
	// vCenter one, and vCenter and ESX move together. Enforcing it removes nothing the
	// vCenter pair has not already removed, and costs a great deal of search to find that
	// out, so it stays out of the constraint set.
	Primary bool
	// Evidence says how this relationship is known.
	Evidence Evidence
}

// Edges are the conceptual relationships, in reading order.
//
// Note these are the *conceptual* edges. Whether upstream actually publishes
// compatibility data for a pair is a separate, live fact read from the cache — see
// Diagram, which takes a coverage lookup.
var Edges = []Edge{
	{
		From: "vcenter", To: "esx", Label: "version pairing",
		Summary: "Upgraded as a pair. vCenter stays at or ahead of its hosts.",
		Prose: "vCenter and ESX are upgraded as a pair, and vCenter must be at or ahead " +
			"of the ESX hosts it manages — so vCenter moves first.",
		Bidirectional: true, Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "vcenter", To: "supervisor", Label: "manages / delivers",
		Summary: "vCenter delivers the Supervisor and sets which versions are available.",
		Prose: "vCenter delivers and manages the Supervisor, and largely determines which " +
			"Supervisor versions are available. Watch the \"vsc\" tail on the Supervisor " +
			"version: it names the release train. vsc9.x ships with vCenter 9.x " +
			"(\"vsc9.1.0.0200\" is literally vCenter 9.1.0.0200); vsc0.x is versioned " +
			"independently. The same Kubernetes version exists on both trains and they are " +
			"not interchangeable — Supervisor 1.31 on vsc9 is a different thing from " +
			"Supervisor 1.31 on vsc0, and a vCenter 8 deployment takes only the latter.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "esx", To: "supervisor", Label: "hosts run it",
		Summary: "The Supervisor runs on the ESX hosts in the cluster.",
		Prose: "The Supervisor control plane and its workloads run on the ESX hosts in the " +
			"cluster, so the host version gates which Supervisor versions can be enabled.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "supervisor", To: "vks", Label: "runs",
		Summary: "VKS runs on the Supervisor and turns it into a cluster service.",
		Prose: "VKS runs on top of the Supervisor and is what turns it into a service that " +
			"can provision guest Kubernetes clusters.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "vks", To: "vkr", Label: "provisions",
		Summary: "VKS provisions the guest clusters and bounds their Kubernetes version.",
		Prose: "VKS provisions guest clusters at a specific Kubernetes release; the VKS " +
			"version declares the Kubernetes minor it serves (the \"+v1.36\" tail), which " +
			"is what bounds the usable VKr versions.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "vcenter", To: "vks", Label: "published directly",
		Summary: "Published, but not a dependency — VKS runs on the Supervisor.",
		Prose: "vCenter is the hub of the published matrix and has a direct compatibility " +
			"edge to VKS, which is what makes it possible to solve a whole stack from a " +
			"single pinned vCenter version. It is not a dependency, though: VKS runs on " +
			"the Supervisor, so enforcing this pair would rule out combinations that " +
			"work. It is looked up, never used to include or exclude.",
		Evidence: EvidencePublished,
	},
	{
		From: "vcenter", To: "vkr", Label: "published directly",
		Summary: "Published, but not a dependency — VKr comes from VKS.",
		Prose: "vCenter also has a direct published edge to VKr, giving a second " +
			"independent reference point. Like the VKS pair it is not a dependency — VKr " +
			"is provisioned by VKS — so it informs rather than constrains.",
		Evidence: EvidencePublished,
	},
	{
		From: "supervisor", To: "vkr", Label: "via VKS",
		Summary: "No published data — worked out through VKS.",
		Prose: "There is no published Supervisor-to-VKr data upstream. The relationship is " +
			"real but has to be inferred through VKS and vCenter, so this tool reports it " +
			"as inferred rather than verified.",
		Evidence: EvidenceInferred,
	},
	{
		From: "vcenter", To: "nsx", Label: "compute manager",
		Summary: "NSX registers vCenter as its compute manager.",
		Prose: "NSX is optional — plenty of vSphere runs without it — but where it is " +
			"deployed the NSX manager registers vCenter as a compute manager, and that " +
			"pairing is versioned. Upstream publishes this pair directly.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "esx", To: "nsx", Label: "published directly",
		Summary: "A real dependency, but broad — vCenter is the pair that decides.",
		Prose: "NSX leans heavily on the ESX hosts: it prepares them as transport nodes, " +
			"and the host version genuinely matters. Upstream publishes the pair, and it " +
			"is still not enforced — not because the dependency is weak, but because the " +
			"published grid is broad. It says yes to very nearly every combination the " +
			"vCenter pair already allows, and vCenter and ESX move together in any case, " +
			"so adding the host pair rules out nothing the vCenter pair does not while " +
			"costing a great deal of search to discover that. NSX is constrained against " +
			"vCenter, the Supervisor and Avi, and nothing else. This pair is looked up " +
			"and reported.",
		Evidence: EvidencePublished,
	},
	{
		From: "nsx", To: "supervisor", Label: "networking",
		Summary: "The Supervisor can run on NSX networking, and then NSX gates it.",
		Prose: "A Supervisor can be enabled on NSX networking or on a vSphere Distributed " +
			"Switch. On NSX, the NSX version gates which Supervisor versions can be " +
			"enabled, and upstream publishes the pair. A Supervisor on VDS has no NSX in " +
			"the picture at all, which is why NSX is optional rather than part of every " +
			"stack.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "vcenter", To: "avi", Label: "cloud connector",
		Summary: "The Avi controller drives vCenter through its cloud connector.",
		Prose: "Avi Load Balancer — the product formerly sold as NSX Advanced Load " +
			"Balancer — talks to vCenter through its vSphere cloud connector to place " +
			"service engines, so the controller version is paired with vCenter. Upstream " +
			"publishes this pair directly.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "nsx", To: "avi", Label: "segments",
		Summary: "When both are deployed, Avi service engines land on NSX segments.",
		Prose: "Where NSX and Avi are deployed together the Avi service engines attach to " +
			"NSX segments, and the two versions are paired. This constrains a stack only " +
			"when both are chosen: Avi on a vSphere Distributed Switch with no NSX " +
			"anywhere is an ordinary deployment, and Avi never requires NSX.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "avi", To: "supervisor", Label: "load balancer",
		Summary: "Avi is the Supervisor load balancer when the Supervisor is on VDS.",
		Prose: "A Supervisor on VDS networking needs an external load balancer, and Avi is " +
			"the supported choice. The pair is published, so where Avi is deployed it " +
			"gates which Supervisor versions can be enabled. A Supervisor on NSX uses NSX " +
			"load balancing instead and has no Avi in the picture.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "esx", To: "avi", Label: "published directly",
		Summary: "Published, but too sparse to enforce — Avi is placed through vCenter.",
		Prose: "Upstream publishes an ESX-to-Avi pair, but it is almost entirely empty: at " +
			"the time of writing three cells in the whole grid say yes, all of them Avi " +
			"32.1.1 against ESX 9.1.x. Enforcing it would collapse every Avi-bearing " +
			"stack to that one combination and rule out deployments that plainly work. " +
			"Avi service engines are placed through vCenter, so vCenter is the pair that " +
			"decides. This one is looked up and reported, never used to include or " +
			"exclude.",
		Evidence: EvidencePublished,
	},
	{
		From: "vcenter", To: "tmc", Label: "management plane",
		Summary: "TMC Self-Managed is versioned against the vCenter it manages.",
		Prose: "TMC Self-Managed is optional — it is installed only where a self-hosted " +
			"management plane is wanted — but where it is deployed it drives vCenter to " +
			"place and manage the clusters it governs, and that pairing is versioned. " +
			"Upstream publishes this pair directly, so it is enforced when TMC is in the " +
			"stack.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "vks", To: "tmc", Label: "manages",
		Summary: "TMC Self-Managed runs on the cluster VKS provisions, and is paired with it.",
		Prose: "TMC Self-Managed is installed on a guest cluster VKS provisions and manages " +
			"the fleet from there, so the VKS version it runs against is paired with it. " +
			"Upstream publishes this pair directly.",
		Primary: true, Evidence: EvidencePublished,
	},
	{
		From: "vkr", To: "tmc", Label: "runs on",
		Summary: "TMC Self-Managed runs on a VKr Kubernetes release, which bounds its version.",
		Prose: "TMC Self-Managed runs on a specific VKr Kubernetes release, and the guest " +
			"cluster's Kubernetes version bounds which TMC versions can be installed. " +
			"Upstream publishes this pair directly.",
		Primary: true, Evidence: EvidencePublished,
	},
}

// Lookups are built once from the tables above. Products is a handful of entries, so a
// linear scan reads fine, but these are called from the solver's inner loop: Constrains
// alone was two scans per edge per call, and a range loop copies each Product, which is
// not a small struct. Indexing once turns the hot path into a map hit.
var (
	byKey        map[string]Product
	byID         map[int]Product
	constrainsBy map[[2]int]bool
)

func init() {
	byKey = make(map[string]Product, len(Products))
	byID = make(map[int]Product, len(Products))
	for _, p := range Products {
		byKey[p.Key] = p
		byID[p.ID] = p
	}

	constrainsBy = make(map[[2]int]bool)
	for _, e := range Edges {
		if !e.Primary {
			continue
		}
		from, fromOK := byKey[e.From]
		to, toOK := byKey[e.To]
		if !fromOK || !toOK {
			continue
		}
		constrainsBy[orderedIDs(from.ID, to.ID)] = true
	}
}

func orderedIDs(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}

// Constrains reports whether a pair is allowed to decide a stack.
//
// This is deliberately not the same as "is a real dependency". ESX × NSX is a real
// dependency that does not constrain, because its published grid is broad enough to rule
// out nothing the vCenter pair does not. vCenter × VKS is the other way round: published,
// enforceable-looking, and not a dependency at all. Both are looked up and reported; only
// the pairs marked Primary include or exclude anything.
func Constrains(aProductID, bProductID int) bool {
	return constrainsBy[orderedIDs(aProductID, bProductID)]
}

// ByKey returns the product with the given short key.
func ByKey(key string) (Product, bool) {
	p, ok := byKey[key]
	return p, ok
}

// ByID returns the product with the given upstream product id.
func ByID(id int) (Product, bool) {
	p, ok := byID[id]
	return p, ok
}

// IDs returns the upstream product ids of every product in scope.
func IDs() []int {
	ids := make([]int, 0, len(Products))
	for _, p := range Products {
		ids = append(ids, p.ID)
	}
	return ids
}

// Pairs returns every unordered product-id pair, with the lower id first. These are the
// twenty-eight pairs `refresh` probes and `pair_coverage` records.
//
// Optional products are included: whether a deployment has NSX or Avi is a question for
// the solver, not for the mirror, and the cache stays a dumb copy of upstream.
func Pairs() [][2]int {
	ids := IDs()
	var out [][2]int
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			a, b := ids[i], ids[j]
			if a > b {
				a, b = b, a
			}
			out = append(out, [2]int{a, b})
		}
	}
	return out
}

// OrderPair returns two products with the lower layer first, so a pair always reads in
// stack order ("Supervisor × VKr") rather than in upstream product-id order.
func OrderPair(a, b Product) (Product, Product) {
	if b.UpgradeOrder < a.UpgradeOrder {
		return b, a
	}
	return a, b
}

// UpgradeOrder returns the product keys in the order a stack should be upgraded.
func UpgradeOrder() []string {
	keys := make([]string, len(Products))
	copy(keys, productKeysSortedByUpgradeOrder())
	return keys
}

func productKeysSortedByUpgradeOrder() []string {
	sorted := make([]Product, len(Products))
	copy(sorted, Products)
	// Insertion sort: five elements, and it keeps the package dependency-free.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].UpgradeOrder < sorted[j-1].UpgradeOrder; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	keys := make([]string, len(sorted))
	for i, p := range sorted {
		keys[i] = p.Key
	}
	return keys
}
