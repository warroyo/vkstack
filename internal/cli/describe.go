package cli

import (
	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/model"
)

// `vkstack describe` is the entry point for a program that has never seen this tool.
//
// An agent handed a binary has two bad options: parse --help, which is prose written for
// people and changes freely, or guess. Neither is a contract. This command is the
// contract — the commands that exist, what they take, what schema comes back, and what
// each exit code means — as one JSON document that changes only when the surface does.
//
// It works without a cache, so an agent can orient itself before it has any data.

func newDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe",
		Short: "Describe this tool's commands, schemas and exit codes as JSON",
		Long: `Describe the agent-facing surface of this CLI.

Intended for programs: every command, its arguments and flags, the schema of what it
returns, and the meaning of every exit code. Needs no cache and makes no network calls.

The web UI is the surface for people; this CLI is the surface for agents.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emit(cmd, "describe", 1, surface())
		},
	}
}

func surface() map[string]any {
	keys := make([]string, 0, len(model.Products))
	for _, p := range model.Products {
		keys = append(keys, p.Key)
	}

	return map[string]any{
		"tool": "vkstack",
		"purpose": "Answer compatibility questions across vCenter, ESX, vSphere " +
			"Supervisor, VKS and VKr from a local mirror of the Broadcom Product " +
			"Interoperability Matrix.",
		"output": map[string]any{
			"default": "json",
			"envelope": map[string]any{
				"schema":   "vkstack.<command>",
				"version":  "integer, bumped on breaking changes to `data`",
				"tool":     "producer and version",
				"snapshot": "which cache the answer came from: fetchedAt, ageHours, stale",
				"data":     "the payload, shaped per command",
			},
			"humanFlag": "--human renders tables and prose instead",
			"env":       "VKSTACK_OUTPUT=human|json|csv sets the default",
			"errors": "failures print a vkstack.error document on stderr and never " +
				"write to stdout, so stdout is either one valid envelope or empty",
		},
		"productKeys": keys,
		"concepts": map[string]any{
			"dependencyChain": []string{"vcenter", "esx", "supervisor", "vks", "vkr"},
			"upgradeOrder":    model.UpgradeOrder(),
			"enforcedPairs": "Only real dependencies constrain a stack. vCenter×VKS and " +
				"vCenter×VKr are published but not dependencies: they are reported, " +
				"never enforced.",
			"unpublishedPairs": "ESX×VKS, ESX×VKr and Supervisor×VKr have no upstream " +
				"data at all. Answers touching them are labelled inferred, never verified.",
			"supportPhases": []string{"general", "technical-guidance", "end-of-support"},
			"testedVsNot": "Status 3 is 'compatible, not tested'. It counts as a yes when " +
				"solving, and is always reported as its own state.",
		},
		"commands": []map[string]any{
			{
				"name": "describe", "schema": "vkstack.describe", "needsCache": false,
				"summary": "This document.",
			},
			{
				"name": "products", "schema": "vkstack.products", "needsCache": true,
				"summary": "Products in scope, their release counts, and which product " +
					"pairs upstream publishes.",
			},
			{
				"name": "releases", "schema": "vkstack.releases", "needsCache": true,
				"args":    []string{"product"},
				"flags":   map[string]string{"--patches": "include patch releases", "--legacy": "include releases past General Support"},
				"summary": "Every release of one product, oldest first, with support phase.",
			},
			{
				"name": "compat", "schema": "vkstack.compat", "needsCache": true,
				"args":    []string{"product", "version"},
				"flags":   map[string]string{"--patches": "include patch releases", "--all-statuses": "include incompatible results"},
				"summary": "The raw pairwise answer for one release, grouped by product.",
			},
			{
				"name": "stack", "schema": "vkstack.stack", "needsCache": true,
				"args": []string{"[product]", "[version]"},
				"flags": map[string]string{
					"--<product>": "pin a version, e.g. --vcenter 8.0U3k (repeat per product)",
					"--patches":   "allow patch releases",
					"--list":      "return several stacks (vkstack.stacks)",
				},
				"summary": "Solve a whole valid stack from one or more pinned versions. " +
					"This is the headline query.",
				"examples": []string{
					"vkstack stack vcenter 8.0U3k",
					"vkstack stack --vcenter 8.0U3k --vks 3.6.2",
				},
				"note": "The positional form takes exactly one product and version. To pin " +
					"more than one product, use the per-product flags.",
			},
			{
				"name": "check", "schema": "vkstack.check", "needsCache": true,
				"flags":   map[string]string{"--vcenter": "version", "--esx": "version", "--supervisor": "version", "--vks": "version", "--vkr": "version"},
				"summary": "Validate a pinned stack pair by pair. Exits 6 when the answer is no.",
				"exitCodes": map[string]int{
					"compatible": ExitOK, "incompatible": ExitIncompatible,
				},
			},
			{
				"name": "explain", "schema": "vkstack.model", "needsCache": false,
				"summary": "The dependency model: products, relationships, how each is " +
					"known, and what this tool does not know.",
			},
			{
				"name": "refresh", "schema": "vkstack.refresh", "needsCache": false,
				"summary": "Pull the matrix into the local cache. The only command that " +
					"makes network calls.",
			},
			{
				"name": "cache info", "schema": "vkstack.cache", "needsCache": false,
				"summary": "Where the cache is, how old it is, and what is in it.",
			},
			{
				"name": "mcp", "needsCache": true,
				"summary": "Serve these queries as MCP tools over stdio, for an agent " +
					"that would rather call a typed tool than shell out.",
			},
			{
				"name": "serve", "needsCache": true,
				"summary": "The web UI, which is the surface for people.",
			},
		},
		"exitCodes": map[string]any{
			"0": map[string]string{"name": "ok", "means": "the command succeeded"},
			"1": map[string]string{"name": "error", "means": "something went wrong"},
			"2": map[string]string{"name": "usage", "means": "the command was called wrongly"},
			"3": map[string]string{"name": "not_found", "means": "no such product or release"},
			"4": map[string]string{"name": "ambiguous", "means": "the version matched several releases"},
			"5": map[string]string{"name": "no_data", "means": "the cache is empty — run refresh"},
			"6": map[string]string{"name": "incompatible", "means": "the question was valid and the answer is no"},
			"7": map[string]string{"name": "no_stack", "means": "no complete stack exists for those pins"},
		},
		"limits": []string{
			"The matrix says which versions may coexist. It says nothing about whether a " +
				"given hop is supported as an upgrade, in what order, or with what downtime.",
			"Answers are only as fresh as the cache; every envelope carries its snapshot.",
			"Releases upstream has not published data for are reported as such, never as " +
				"'nothing is compatible'.",
		},
	}
}
