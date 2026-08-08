// Package model holds the conceptual dependency model: which products exist, how they
// relate, and why. It is the single source of truth behind the mermaid diagram, the
// generated docs/model.md, `interop explain`, and the web whiteboard view.
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
}

// Products are the five components in scope, in diagram order.
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
		Key: "supervisor", ID: 1378, Name: "VMware vSphere Supervisor", Label: "Supervisor",
		Scheme: SchemeGeneric, Example: "v1.32.9+vmware.2-fips-vsc9.1.0.0200",
		K8sMinorRun: 0, UpgradeOrder: 3,
	},
	{
		Key: "vks", ID: 1794, Name: "vSphere Kubernetes Service", Label: "VKS",
		Scheme: SchemeGeneric, Example: "3.7.0+v1.36",
		K8sMinorRun: 1, UpgradeOrder: 4,
	},
	{
		Key: "vkr", ID: 820, Name: "vSphere Kubernetes releases", Label: "VKr",
		Scheme: SchemeGeneric, Example: "1.36.1",
		K8sMinorRun: 0, UpgradeOrder: 5,
	},
}

// Edge is a conceptual relationship between two products.
type Edge struct {
	From  string // product key
	To    string // product key
	Label string // short arrow label for the diagram
	// Prose explains what the dependency actually is, in one sentence.
	Prose string
	// Bidirectional marks a mutual version-pairing constraint rather than a
	// "this one determines that one" direction.
	Bidirectional bool
	// Primary marks the edges that form the backbone of the explanation. Non-primary
	// edges exist in the data but are shortcuts rather than the conceptual spine.
	Primary bool
}

// Edges are the conceptual relationships, in reading order.
//
// Note these are the *conceptual* edges. Whether upstream actually publishes
// compatibility data for a pair is a separate, live fact read from the cache — see
// Diagram, which takes a coverage lookup.
var Edges = []Edge{
	{
		From: "vcenter", To: "esx", Label: "version pairing",
		Prose: "vCenter and ESX are upgraded as a pair, and vCenter must be at or ahead " +
			"of the ESX hosts it manages — so vCenter moves first.",
		Bidirectional: true, Primary: true,
	},
	{
		From: "vcenter", To: "supervisor", Label: "manages / delivers",
		Prose: "vCenter delivers and manages the Supervisor, and largely determines which " +
			"Supervisor versions are available. Watch the \"vsc\" tail on the Supervisor " +
			"version: it names the release train. vsc9.x ships with vCenter 9.x " +
			"(\"vsc9.1.0.0200\" is literally vCenter 9.1.0.0200); vsc0.x is versioned " +
			"independently. The same Kubernetes version exists on both trains and they are " +
			"not interchangeable — Supervisor 1.31 on vsc9 is a different thing from " +
			"Supervisor 1.31 on vsc0, and a vCenter 8 deployment takes only the latter.",
		Primary: true,
	},
	{
		From: "esx", To: "supervisor", Label: "hosts run it",
		Prose: "The Supervisor control plane and its workloads run on the ESX hosts in the " +
			"cluster, so the host version gates which Supervisor versions can be enabled.",
		Primary: true,
	},
	{
		From: "supervisor", To: "vks", Label: "runs",
		Prose: "VKS runs on top of the Supervisor and is what turns it into a service that " +
			"can provision guest Kubernetes clusters.",
		Primary: true,
	},
	{
		From: "vks", To: "vkr", Label: "provisions",
		Prose: "VKS provisions guest clusters at a specific Kubernetes release; the VKS " +
			"version declares the Kubernetes minor it serves (the \"+v1.36\" tail), which " +
			"is what bounds the usable VKr versions.",
		Primary: true,
	},
	{
		From: "vcenter", To: "vks", Label: "published directly",
		Prose: "vCenter is the hub of the published matrix and has a direct compatibility " +
			"edge to VKS, which is what makes it possible to solve a whole stack from a " +
			"single pinned vCenter version.",
	},
	{
		From: "vcenter", To: "vkr", Label: "published directly",
		Prose: "vCenter also has a direct published edge to VKr, giving a second " +
			"independent constraint on the guest cluster version.",
	},
	{
		From: "supervisor", To: "vkr", Label: "inferred via VKS",
		Prose: "There is no published Supervisor-to-VKr data upstream. The relationship is " +
			"real but has to be inferred through VKS and vCenter, so this tool reports it " +
			"as inferred rather than verified.",
	},
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
// pairs `refresh` probes and `pair_coverage` records.
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
