---
name: vkstack-upgrade-plan
description: Build a step-by-step upgrade plan for a vSphere / VCF stack (vCenter, ESX, vSphere Supervisor, VKS, VKr guest clusters, and optionally Avi Load Balancer, NSX, TMC Self-Managed) using the vkstack MCP server. Produces an ordered path where every intermediate state is valid in the Broadcom interoperability matrix, checked against Broadcom upgrade docs and release notes, with a mermaid flow diagram and doc sources. Use this whenever someone asks how to get from one vSphere/VCF/Supervisor/VKS/TKr/Avi version to another, wants an upgrade sequence, upgrade path, migration plan or "hop" plan, asks whether a proposed upgrade order is valid, or mentions moving from vSphere 8 to VCF 9 — even if they only name one or two of the components.
---

# vkstack upgrade plan

Turn a current stack and a target into an ordered upgrade path where **every state in between is a valid stack**, backed by the Broadcom interoperability matrix (through the vkstack MCP server) and by Broadcom's upgrade documentation.

The matrix says which versions may *coexist*. It does not say which hops are supported *upgrades*. A good plan needs both: vkstack for coexistence, and release notes and KBs for hop legality and order. Keep the two apart in the output, so a reader can tell which claim rests on which source.

## Tools

Everything here goes through the vkstack MCP tools, which answer from a local cache with no network calls:

| Tool | Use it for |
|---|---|
| `vkstack_model` | Once per session: which pairs are real dependencies, and what the tool doesn't know |
| `vkstack_releases` | Exact release strings, support phase, and whether a version exists at all |
| `vkstack_stack` | The target stack, and **compatibility windows**: pin what you know, read `options` for everything else |
| `vkstack_check` | Validate one full state; call it once per step |
| `vkstack_path` | If listed: the shortest matrix-valid route between two stacks, in one call. Prefer it over hand-building (step 5) |
| `vkstack_compat` | Rarely. The raw pairwise list for one release, which is large for vCenter/ESX. Prefer `vkstack_stack` windows |

If the MCP server isn't connected, the same operations exist on the `vkstack` CLI (`stack`, `check`, `path`, `releases`), which return the same JSON envelopes.

## Data sources: what to use and what to ignore

- **Use:** vkstack, Broadcom TechDocs release notes, Broadcom KB articles, and the user's own operational knowledge (label it as such).
- **Ignore the VCF Upgrade Planner and its output.** The interop matrix is taking over that data, including older VKS releases such as 3.0.0. If a user pastes Upgrade Planner output, you can compare against it, but don't take versions or orderings from it. If the matrix lacks a version, say so and check the release notes instead.

## Workflow

### 1. Preflight

- Every vkstack result carries a `snapshot`. If `stale` is true, say so and suggest `vkstack refresh`. If the cache is empty, tools fail with `no_data`. Refreshing is the operator's call, since it's the only networked operation.
- Carry `snapshot.fetchedAt` into the plan. A compatibility claim without a date can't be checked.
- Call `vkstack_model` once if you haven't this session. Its dependency list and `unknowns` prevent the most common wrong conclusions.

### 2. Resolve every version to an exact matrix string

Versions resolve by exact match, then unique prefix. User-supplied strings often don't match, and a `release_not_found` error names the newest version as a hint:

- **Supervisor:** drop the trailing build number. `v1.28.3+vmware.2-fips.1-vsc0.1.9-23905380` becomes `v1.28.3+vmware.2-fips.1-vsc0.1.9`.
- **VKr/TKr:** older releases carry labels. `v1.28.8+vmware.1-fips.1` becomes `1.28.8 (TKr 1.28.8 for vSphere 8.x)`. Find them with `vkstack_releases` using `product: "vkr"` and `legacy: true`.
- **Not in the matrix** (for example Avi 22.1.6 or VKS 3.0.0): use the nearest listed release as a stand-in only with the user's agreement, and flag the assumption everywhere it matters. Never invent pairs.
- **Never set `hidePatches`.** Patch letters decide answers: vCenter 8.0U3 takes Supervisor 1.26–1.28, 8.0U3g up to 1.30, 8.0U3i up to 1.32.

### 3. Solve the target

Call `vkstack_stack` with the target pinned (usually the target vCenter), and put each optional product the user runs in `include` (`avi`, `nsx`, `tmc`). Note which target builds are forced. For example, vCenter 9.1.1.0 takes only Avi 32.1.3/32.1.4 and lists Supervisor v1.33.13 vsc9.1.1.0, not 1.34.9.

### 4. Map the gates with windows

A **window** is what one layer can still be once other layers are pinned. `vkstack_stack` returns it in `options`:

```
vkstack_stack  pins={"vcenter": "8.0U3g", "avi": "22.1.7"}  include=["avi"]
  → options.supervisor = v1.28.3…vsc0.1.9 … v1.30.10…vsc0.1.12
    options.esx        = 8.0U3 … 8.0U3f        (8.0U3g+ would need a newer Supervisor)
```

Ask the questions that decide the order:

- For each candidate vCenter build, which Supervisors are in its window?
- For each optional-product release, which vCenter and Supervisor builds allow it? Pin the product, and read `options.vcenter` and `options.supervisor`.
- Which VKS releases allow the current and next guest-cluster versions?

Look especially for **bridge releases**: the one build of a product whose window spans both sides of a transition. Avi 32.1.2 is the only Avi that pairs with both vsc0 and vsc9 Supervisors. Paths often hinge on one of these.

`options` is the set that still fits in *some* complete stack given the pins. It is not a proof that a particular combination works. Confirm combinations with `vkstack_check`.

### 5. Build the backbone path

If `vkstack_path` is available, call it with the current and target stacks (vCenter, Supervisor, and any optional products) and use its route as the backbone. It returns the shortest sequence of single-component moves where every state is valid, and says so when no route exists.

Otherwise, build the backbone by hand from the windows:

- Walk vCenter forward only as far as the Supervisor needs. Each vCenter hop should unlock the next Supervisor minor, or an optional-product hop.
- Move the Supervisor one Kubernetes minor at a time. The vsc0 → vsc9 train switch happens at the same minor, after vCenter reaches 9.x.
- Move each optional product when, and only when, the next vCenter or Supervisor hop would leave its window. Jump straight to the release you need, if its upgrade checklist allows (step 8).
- Before each move, check that the new state is inside every window. If no single move is valid, look for a bridge release.

A hand-built path is valid but not guaranteed shortest. Say so if the user asks whether it's optimal, and name the steps that might be removable.

### 6. Layer ESX, VKS and guest clusters (VKr) onto the backbone

- **Guest clusters (VKr) are constrained by VKS alone.** VKr is provisioned by VKS, so VKS × VKr is the only pair that matters for clusters. Bump VKr one Kubernetes minor at a time, keeping each version inside the running VKS's window. Supervisor × VKr is unpublished and is not a dependency; don't tie cluster versions to the Supervisor's own Kubernetes minor.
- **Plan the guest clusters as one round.** Because VKr depends only on VKS, the cluster work (VKr bumps plus the VKS hops they need) can be done as one contiguous block: clusters up to the next VKS's floor, the VKS hop, then clusters on to the target. Place that round in the single numbered sequence, not on a parallel track. Find its **gate** and **deadline**. The gate is the earliest infrastructure step a VKS hop needs, e.g. VKS 3.6.3 needs Supervisor ≥ 1.30. The deadline is the last infrastructure step before which a VKS hop must be done, because the old VKS doesn't pair with the next Supervisor, e.g. VKS 3.3.3 doesn't pair with vsc9. Put the round right after the gate, and say it can slide anywhere up to the deadline so app teams can test on their own schedule. Verify the round with `vkstack_check` at both ends of that window. Running it early gets VKS and clusters onto supported releases sooner.
- **VKS moves when the next VKr or Supervisor step needs it.** Check the VKS window (pin the Supervisor and the VKr), then the VKS release notes for upgrade prerequisites. For example, VKS 3.6 needs VKS ≥ 3.3 installed, all clusters on VKr ≥ 1.32, and Supervisor ≥ 1.30.
- **ESX follows vCenter, but check its window.** ESX 8.0U3g and later drop old vsc0 Supervisors (below 1.30/vsc0.1.13), so holding ESX on 8.0U3 while the Supervisor climbs is often necessary. ESX 8 pairs only with vsc0 Supervisors, and ESX 9 only with vsc9.
- **8.x → 9.x order: Supervisor to vsc9 first, then ESX to 9.x.** Broadcom's 9.1 upgrade order is vCenter → VKS → Supervisor → ESX. The state in between (ESX 8 + vsc9 Supervisor) fails `vkstack_check`, because the matrix has no such pairs. Mark it **transitional**, not incompatible, and split it into two steps (e.g. 15a/15b).

### 7. Validate every state

Call `vkstack_check` once per step, pinning the **full** stack after that step (every product the user runs, leaving out only versions the matrix doesn't list). Keep the results as you go. From each result:

- **Any enforced pair failing** (`dependency: true`, status not 1 or 3): the plan is wrong, unless this is the marked transitional step. Fix the plan; don't paper over it.
- **"Compatible, not tested"** (status 3): list these pairs with their steps in the plan.
- **Informational no** (`dependency: false`, e.g. vCenter × VKr): mention it, but don't let it drive the order. ESX × Avi is near-empty upstream; ignore it.
- **Unpublished pairs** (e.g. Supervisor × VKr) aren't failures, and they aren't dependencies either. Don't invent a constraint to fill the gap.

An incompatible state is an answer, not a malfunction: read the payload.

### 8. Check hop legality in Broadcom docs

Fetch and cite the actual pages; don't rely on memory. What to check, and the traps, are in `references/doc-checks.md`. At minimum:

- **vCenter back-in-time rules.** The target build must be newer than the source. For example, 8.0U3j/k cannot go to 9.1.0.x but can go to 9.1.1.0, while 8.0U3–U3i can go to 9.1.0. If the path needs an intermediate vCenter, make sure the source patch level can reach it, and tell the user not to patch vCenter 8 past that level.
- **The optional product's upgrade checklist** for the target line, which gives the supported source versions. For example, Avi 22.1.4–22.1.7 → 32.1.x is direct. If a direct hop is supported, drop intermediate stops; each one is a maintenance window.
- **VKS release notes** for each VKS hop's prerequisites.
- **Known issues** in the target release notes that apply to the hops you chose.
- **Pre-upgrade blockers** for the target vCenter line. For example, KB 455737 (the VUM database schema check) must pass before any 8.0U3 or 9.0 → 9.1 vCenter hop. Put these in the pre-flight checklist and on the vCenter step they block.

### 9. Write the plan

Default to a Claude Doc when a docs connector is available (otherwise markdown). Offer two versions when the audience includes a customer:

- **Detailed (internal):** summary, mermaid flow, the constraints that set the order (window tables), per-phase step tables with the **full stack after each step**, not-tested and transitional notes, risks and open questions, and sources.
- **Customer-facing:** overview (current → target table), mermaid flow, one compact step table (step · phase · component · upgrade-to), a "before you start" checklist, key cautions, and a references table. Leave out tool internals, window tables, per-step stack columns, informational pairs and "compatible, not tested" callouts: a compatible pair is enough for the customer, so keep not-tested detail in the internal doc only, with no dashed styling in the customer diagram.

**Mermaid diagram:** `flowchart LR`, one `subgraph` per phase with `direction TB` inside, and phases linked subgraph-to-subgraph (`START --> P1 --> P2 ...`) so the internal direction holds. Use a dashed style for steps with not-tested pairs and a highlighted style for the transitional step. Label clusters "Guest clusters", never bare "VKr", so nobody reads them as Supervisor steps.

**Group steps into phases by vCenter version.** Each phase is "what happens while vCenter is on X". That's how operators schedule the work. Number every step in one sequence (1, 2, 3 …, with a/b for the transitional split). In mermaid, draw the guest cluster round as its own subgraph ("Guest cluster round · vCenter X"), shaded with `style CL fill:…`. Put it inline in the main chain (`START --> P1 --> P2 --> CL --> P3 …`), so the whole diagram reads as one left-to-right line. Avoid parallel lanes and dotted side links: readers found a separate cluster track, and cluster steps scattered across phases, hard to follow. Linking nodes across subgraphs makes mermaid ignore `direction TB`.

**Sources:** link every doc you fetched beside the claim it supports, and in a Sources/References section at the end. Include the matrix snapshot date.

## Pitfalls that have cost time before

- **Same line, different compatibility.** Avi 31.2.2 pairs with vsc0 Supervisors, but 31.2.1 and 31.2.3 don't. Avi 32.1.2 pairs with vsc0, but 32.1.1 and 32.1.3 don't. Avi 32.1.3 pairs with vsc9.1.0.0, not vsc9.1.0.0300. Always name exact builds. The vkstack web UI groups Avi and NSX by major.minor and pins the newest build on click, which hides this, so don't send people to the UI to "pick 31.2".
- **A Supervisor build that fits vCenter can still fail an optional product.** For example, Supervisor 1.31 vsc0.1.13 fails with Avi 31.2.2, while vsc0.1.14 works. Re-check after any build substitution.
- **Removing an intermediate vCenter can break an optional product.** An intermediate vCenter is often there only because Avi or NSX needs it. Re-run `vkstack_check` before simplifying the route.
- **`vkstack_compat` on a vCenter or ESX release can exceed the tool-result limit.** Use `vkstack_stack` windows instead.

## Output checklist

Before handing over:

- [ ] Every step names exact builds.
- [ ] Every state was checked with `vkstack_check`, with results and snapshot date recorded.
- [ ] The transitional step is marked, and its order is sourced.
- [ ] Every guest-cluster version sits inside the running VKS's window, and clusters move one minor at a time.
- [ ] Back-in-time limits are checked for every vCenter hop, and "don't patch past X" is stated when it applies.
- [ ] Pre-upgrade blocker KBs for each vCenter hop (e.g. KB 455737 before 9.1) are in the pre-flight checklist.
- [ ] Each optional-product hop is checked against its own upgrade checklist.
- [ ] Not-tested pairs are listed with their steps.
- [ ] Assumptions (stand-in versions, versions missing from the matrix) are stated.
- [ ] Doc links sit next to the claims they support, and are collected at the end.
