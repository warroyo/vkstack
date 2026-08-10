package model

import "fmt"

// An Unknown is something this tool cannot tell you, stated plainly.
//
// These are deliberately spelled out rather than glossed. A compatibility tool that
// quietly presents a gap as an answer is worse than no tool, so every limit gets a name,
// what it means in practice, and what to do instead.
type Unknown struct {
	// Title names the limit in a few words.
	Title string
	// What it is.
	Detail string
	// Consequence is what it means when you are using the answer.
	Consequence string
	// Workaround is what to do about it, or "" when there is nothing to do.
	Workaround string
}

// Unknowns returns everything the tool does not know, in the order worth reading.
//
// The list is derived from the model where possible — the unpublished pairs come from the
// recorded coverage — so it cannot silently drift out of date if upstream changes.
func Unknowns(covered Coverage) []Unknown {
	var out []Unknown

	for _, pr := range Pairs() {
		if covered(pr[0], pr[1]) {
			continue
		}
		first, _ := ByID(pr[0])
		second, _ := ByID(pr[1])
		a, b := OrderPair(first, second)
		u := Unknown{
			Title: fmt.Sprintf("%s × %s is not published", a.Label, b.Label),
			Detail: fmt.Sprintf(
				"The interoperability matrix returns nothing at all for %s against %s — not "+
					"an empty result, but no data. Nobody has stated whether these two work together.",
				a.Label, b.Label),
		}
		switch {
		case a.Key == "supervisor" && b.Key == "vkr", a.Key == "vkr" && b.Key == "supervisor":
			u.Consequence = "This is the gap that matters. It is a question people genuinely " +
				"ask, and there is no direct answer to look up."
			u.Workaround = "This tool answers it indirectly: VKS sits between them and is " +
				"published against both, and vCenter is published against both, so a " +
				"combination that satisfies those is reported — always labelled inferred, " +
				"never as verified compatible."
		default:
			u.Consequence = "In practice this costs nothing: it is not a combination anyone " +
				"configures directly."
			u.Workaround = "Constrain through vCenter and Supervisor instead, which are published."
		}
		out = append(out, u)
	}

	out = append(out,
		Unknown{
			Title: "Which optional components a deployment actually has",
			Detail: "NSX and Avi are each present in some deployments and absent from others, " +
				"and nothing upstream says which. A Supervisor can run on NSX networking or on " +
				"a vSphere Distributed Switch with Avi in front of it, or on neither.",
			Consequence: "A solved stack leaves NSX and Avi out unless you ask for them, so an " +
				"answer that does not mention NSX is not a claim that you have no NSX — it is " +
				"a claim about the five components that are always there.",
			Workaround: "Pin a version (`--nsx 9.1.0.0200`) or opt in by name " +
				"(`--with nsx`, `--with avi`, `--with nsx,avi`). The two are independent: " +
				"asking for one never pulls in the other.",
		},
		Unknown{
			Title: "Whether Avi and ESX versions really are that restricted",
			Detail: "Upstream publishes an ESX × Avi pair, but almost every cell in it is " +
				"empty — at the time of writing three say yes, all of them Avi 32.1.1 against " +
				"ESX 9.1.x. Avi is not deployed onto hosts directly; its service engines are " +
				"placed through vCenter.",
			Consequence: "This tool reports the pair but never enforces it. Enforcing it would " +
				"rule out Avi deployments that plainly work, so a stack can be called valid " +
				"even though the ESX × Avi cell is blank.",
			Workaround: "`vkstack compat avi <version>` shows the pair as it is published, " +
				"blanks and all.",
		},
		Unknown{
			Title: "Whether an upgrade is actually safe to perform",
			Detail: "The matrix says which versions may coexist. It says nothing about " +
				"order, downtime, data migration, or whether a given hop is supported as an " +
				"upgrade rather than as a fresh install.",
			Consequence: "A stack this tool calls valid is a valid destination. It is not a " +
				"statement that you can get there from where you are.",
			Workaround: "Treat the upgrade ordering here as a starting point and confirm " +
				"against the product upgrade documentation before executing.",
		},
		Unknown{
			Title: "Anything about releases upstream has not published yet",
			Detail: "Unreleased versions appear in the product list before any compatibility " +
				"data exists for them.",
			Consequence: "They show as \"no data yet\" rather than being hidden, so you can " +
				"see that a version exists without being told anything false about it.",
			Workaround: "Re-run `vkstack refresh` once the release ships.",
		},
		Unknown{
			Title: "Whether \"compatible, not tested\" will work for you",
			Detail: "The matrix distinguishes tested-compatible from compatible-but-not-tested. " +
				"This tool treats both as a yes.",
			Consequence: "A stack can be reported valid on the strength of a combination " +
				"nobody has actually run.",
			Workaround: "`vkstack compat <product> <version>` marks which results are " +
				"not tested.",
		},
		Unknown{
			Title: "Older releases, by choice",
			Detail: "vCenter and ESX below 8.0 U3 are filtered out, and Supervisor, VKS and " +
				"VKr are dropped when nothing above that floor can reach them.",
			Consequence: "The answers are scoped to currently relevant versions, not to " +
				"everything the matrix knows.",
			Workaround: "`--all-versions` disables the floor; `--min-version vcenter=9.0.0.0` moves it.",
		},
	)
	return out
}

// GroupingNotes explains where the views summarise rather than show every release, so a
// reader knows when they are looking at a line and when at an exact build.
func GroupingNotes() []string {
	return []string{
		"Supervisor, VKS and VKr are grouped by version line — one node covers several " +
			"builds, and the count is shown on the node. Hover it for the exact list.",
		"Supervisor is additionally split by release train (vsc9 and vsc0), because the " +
			"same Kubernetes version on the two trains is not the same thing.",
		"vCenter is never grouped. Its patch letter changes what it supports: 8.0U3 takes " +
			"Supervisor 1.26–1.28, while 8.0U3k takes 1.31–1.33.",
		"ESX is not shown as a layer. Its release lines mirror vCenter's and it has no " +
			"published data against VKS or VKr, so it appears as the hosts each vCenter runs on.",
		"NSX and Avi are grouped by their major.minor line — NSX 9.1 covers 9.1.0.0 " +
			"through 9.1.0.0200, Avi 32.1 covers 32.1.1 and 32.1.2.",
		"NSX and Avi are optional layers and start collapsed. Expanding or pinning one " +
			"has no effect on the other: they are separate choices, and Avi does not " +
			"require NSX.",
	}
}
