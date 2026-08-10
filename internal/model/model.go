// Package model holds the conceptual dependency model: which products exist, how they
// relate, and why. It is the single source of truth behind the mermaid diagram, the
// generated docs/model.md, `vkstack explain`, and the web whiteboard view.
//
// The prose here is authored domain knowledge, not derived from the API. The version
// evidence supports the Supervisor<->vCenter coupling (versions embed the vCenter line,
// e.g. "-fips-vsc9.1.0.0200") and the VKS<->Kubernetes coupling ("3.7.0+v1.36"); the rest
// is reviewable and expected to be corrected in place.
package model

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
	// Optional products are independent of each other. NSX and Avi are each opted into on
	// their own, and all four combinations — neither, either one alone, both — are real
	// deployments. Nothing may treat one as implying the other.
	Optional bool
}

// Products are the seven components in scope, in diagram order.
//
// Five are always part of a stack. NSX and Avi are Optional: they sit between the
// hypervisor and the Supervisor, they are opted into individually, and a stack without
// either is still a complete answer.
//
// VCF is deliberately absent: it is a wrapper bill-of-materials over these component
// versions, not an independent compatibility axis.
var Products = []Product{
	{
		Key: "vcenter", ID: 2, Name: "VMware vCenter", Label: "vCenter",
		Scheme: SchemeVSphere, Example: "8.0U3k", MinVersion: "8.0U3",
		K8sMinorRun: -1, UpgradeOrder: 1,
	},
	{
		Key: "esx", ID: 1, Name: "VMware ESX", Label: "ESX",
		Scheme: SchemeVSphere, Example: "8.0U3k", MinVersion: "8.0U3",
		K8sMinorRun: -1, UpgradeOrder: 2,
	},
	{
		Key: "nsx", ID: 912, Name: "VMware NSX", Label: "NSX",
		Scheme: SchemeVSphere, Example: "9.1.0.0200",
		K8sMinorRun: -1, UpgradeOrder: 3, Optional: true,
	},
	{
		Key: "avi", ID: 1795, Name: "Avi Load Balancer", Label: "Avi",
		Scheme: SchemeGeneric, Example: "32.1.2",
		K8sMinorRun: -1, UpgradeOrder: 4, Optional: true,
	},
	{
		Key: "supervisor", ID: 1378, Name: "VMware vSphere Supervisor", Label: "Supervisor",
		Scheme: SchemeGeneric, Example: "v1.32.9+vmware.2-fips-vsc9.1.0.0200",
		K8sMinorRun: 0, UpgradeOrder: 5,
		Trains: []string{"vsc9", "vsc0"},
		TrainNote: "vsc9.x ships with vCenter 9.x; vsc0.x is versioned independently. " +
			"The same Kubernetes version exists on both and they are not interchangeable.",
	},
	{
		Key: "vks", ID: 1794, Name: "vSphere Kubernetes Service", Label: "VKS",
		Scheme: SchemeGeneric, Example: "3.7.0+v1.36",
		K8sMinorRun: 1, UpgradeOrder: 6,
	},
	{
		Key: "vkr", ID: 820, Name: "vSphere Kubernetes releases", Label: "VKr",
		Scheme: SchemeGeneric, Example: "1.36.1",
		K8sMinorRun: 0, UpgradeOrder: 7,
	},
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
	// Primary marks a real dependency: one component actually runs on, is delivered by,
	// or is provisioned by the other.
	//
	// This is also the constraint set. The matrix publishes pairs that are not
	// dependencies — vCenter against VKS and against VKr — and those must not be
	// enforced when validating a stack: VKS does not run on vCenter, it runs on the
	// Supervisor, so Supervisor is what decides whether a VKS version is usable.
	// Enforcing the non-dependency pairs produces combinations that are listed
	// compatible yet cannot exist.
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
		From: "esx", To: "nsx", Label: "transport nodes",
		Summary: "ESX hosts are prepared as NSX transport nodes.",
		Prose: "NSX prepares the ESX hosts as transport nodes and installs its data plane " +
			"on them, so the host version gates which NSX versions can be deployed.",
		Primary: true, Evidence: EvidencePublished,
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
}

// IsDependency reports whether two products have a real dependency between them, and so
// whether their compatibility should constrain a stack.
//
// Pairs the matrix publishes that are not dependencies (vCenter against VKS or VKr) are
// informational: useful to look up, wrong to enforce.
func IsDependency(aProductID, bProductID int) bool {
	for _, e := range Edges {
		if !e.Primary {
			continue
		}
		from, _ := ByKey(e.From)
		to, _ := ByKey(e.To)
		if (from.ID == aProductID && to.ID == bProductID) ||
			(from.ID == bProductID && to.ID == aProductID) {
			return true
		}
	}
	return false
}

// ByKey returns the product with the given short key.
func ByKey(key string) (Product, bool) {
	for _, p := range Products {
		if p.Key == key {
			return p, true
		}
	}
	return Product{}, false
}

// ByID returns the product with the given upstream product id.
func ByID(id int) (Product, bool) {
	for _, p := range Products {
		if p.ID == id {
			return p, true
		}
	}
	return Product{}, false
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
// twenty-one pairs `refresh` probes and `pair_coverage` records.
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
