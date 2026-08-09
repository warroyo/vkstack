# vkstack

Compatibility across **vCenter, ESX, vSphere Supervisor, VKS and VKr**, from the Broadcom
Product Interoperability Matrix.

The matrix answers one question at a time — "is A compatible with B". This answers the
question people actually have: *what is the whole valid stack?*

Single Go binary, no cgo, no runtime dependencies. Data is cached locally, so everything
after the first `refresh` works offline.

## Two surfaces, two audiences

**People use the web UI. Agents use the CLI.** That split is deliberate, and it decides
how each side is built.

A person asking "what can I run with vCenter 8.0U3k" is served far better by a map they
can click through than by a table — so `serve` gets the interaction, the provenance
shading and the support-lifecycle colouring. A program asking the same question wants
data it does not have to scrape — so every CLI read command emits a versioned JSON
envelope **by default**, errors are JSON with stable codes, and exit codes separate a bad
question from a well-formed one whose answer is no. `--human` brings the tables back.

For agents there is also `vkstack describe`, which states the whole surface as JSON, and
`vkstack mcp`, which serves the same queries as MCP tools over stdio. See
[AGENTS.md](AGENTS.md).

## Quick start

```sh
go install github.com/warroyo/vkstack/cmd/vkstack@latest   # or build: go build ./cmd/vkstack

vkstack refresh                    # pull the matrix into a local cache (~1 min)
vkstack serve --open               # the map, for a person
vkstack stack vcenter 8.0U3k       # the whole valid stack, as JSON
vkstack stack vcenter 8.0U3k --human
```

Prebuilt binaries for each platform are on the
[releases page](https://github.com/warroyo/vkstack/releases). For wiring it into an agent —
`claude mcp add vkstack -- vkstack mcp` — see [AGENTS.md](AGENTS.md).

## Commands

| Command | What it does |
|---|---|
| `describe` | The whole agent-facing surface — commands, schemas, exit codes — as JSON |
| `explain` | The dependency model (JSON; `--human` for prose, `--ascii` for a bare terminal) |
| `refresh` | Pull the matrix into `~/.cache/vkstack/vkstack.db` |
| `stack <product> <version>` | Solve a whole valid stack from one or more pinned versions |
| `compat <product> <version>` | The raw pairwise answer for one release |
| `check --vcenter … --esx …` | Validate a fully pinned stack; exit 6 if incompatible |
| `products` / `releases` | What is in scope, and which pairs upstream publishes |
| `serve` | Web UI — the stack map, local by default or hosted (see below) |
| `mcp` | Serve the queries as MCP tools over stdio, for an agent |
| `cache info\|path\|clear` | Inspect or drop the cache |

Output is JSON unless you ask otherwise: `--human` for tables and prose, `--csv` on the
commands whose answer really is rows (`releases`, `compat`, `products`), or
`VKSTACK_OUTPUT=human` to set a default for your shell. Product keys are `vcenter`,
`esx`, `supervisor`, `vks`, `vkr`.

## The stack map

`vkstack serve` opens on a layered map of the whole stack, drawn bottom-up: vCenter at
the base, branching up through Supervisor, VKS and VKr. Pick any version at any layer and
the map redraws around it, with a list underneath showing every version in every layer,
lit or faded.

**Only real dependencies constrain a stack.** The chain is:

```
VKr  →  VKS  →  Supervisor  →  vCenter  ↔  ESX
```

The matrix also publishes vCenter against VKS and vCenter against VKr. Those are worth
looking up, but they are not dependencies — VKS runs on the Supervisor, and VKr is
provisioned by VKS — so they are reported and never enforced. Enforcing them produces
combinations that are listed compatible yet cannot exist: vCenter 9.0.0.0 and VKS 3.7 are
listed together, but vCenter 9.0.0.0 tops out at Supervisor 1.30 while VKS 3.7 needs
1.32, so there is no Supervisor to put in the middle.

A node is lit when a complete valid stack exists containing it and your selection,
enforcing the chain above. `vkstack check` reports the non-dependency pairs separately,
under "not part of the verdict".

A node usually stands for several releases, and clicking it selects the newest — the peek
on hover says which one, and lists the rest. Where the matrix hedges, the picture hedges
with it: a **dashed** connector means upstream lists the combination compatible without
having tested it, and a **dotted** one means it published no result for that pair and the
link is inferred from the rest of the stack. Upstream footnotes on a result — "Running
Supervisor is compatible after VC upgrade", for one — are shown rather than dropped.

Three things are grouped deliberately:

- **Supervisor is split by release train.** The same Kubernetes version ships on two
  trains that are *not* interchangeable: `vsc9.x` ships with vCenter 9.x, `vsc0.x` is
  versioned independently. Supervisor 1.31 on vsc9 will not run on a vCenter 8
  deployment. Each train is its own node, badged `vsc9` / `vsc0`, so the two never read
  as one version. Which trains a vCenter accepts comes from the matrix, not a rule —
  vCenter 9.1.0.0300 accepts both.

- **vCenter is not collapsed by patch.** 8.0U3 supports Supervisor 1.26–1.28 while 8.0U3k
  supports 1.31–1.33, so hiding the patch letter would throw away the answer.
- **ESX is not a layer.** Its release lines are identical to vCenter's and it has no
  published data against VKS or VKr, so it appears as "on ESX 9.1 · 9.0 · 8.0U3" under
  each vCenter node instead of a row nobody can branch from.

### Support lifecycle

The matrix publishes a support phase on every release, and the map colours it:

| | |
|---|---|
| **General Support** | normal |
| **Technical Guidance** | amber, tagged `TG` — General Support has ended, no new fixes |
| **End of Support** | red and struck through, tagged `EOS` |

"Legacy" means the same thing here as on the interop site: nothing left in General
Support. The site's *hide legacy releases* checkbox maps onto the same two flags this
uses (`isHideGenSupported` and `isHideTechSupported`), so the **Hide legacy releases**
toggle mirrors it, on by default. On the CLI, `vkstack releases <product> --legacy`
includes them.

A grouped node keeps the best phase among its releases as its headline, because the line
really is still usable — but it names its worst too, so a green node covering an End of
Support build cannot be picked from blind.

The toggle filters the *picture*, not the solver. So "Newest working stack from here" can
name a release the map is hiding; it carries the phase of every release in it and says
when it reached past the filter, rather than reading as though everything shown is in
General Support.

Releases upstream has not published anything for yet — 9.2.0.0 at the time of writing —
are marked "no data yet" rather than silently omitted.

## Hosting a shared instance

By default `serve` binds `127.0.0.1` and only refreshes when asked, which is what you want
on a laptop. For a shared instance, bind wider, make it read-only, and let the server keep
itself current:

```sh
vkstack serve --bind 0.0.0.0 --port 8080 --read-only --refresh-interval 6h
```

- `--read-only` rejects client-triggered refreshes with a 403, so visitors cannot make the
  server call upstream. Everything else still works — it is read-only to *clients*, while
  the scheduled refresh keeps writing to the cache.
- `--refresh-interval` sets the cadence at runtime. A cold cache refreshes immediately on
  start; a warm one waits for the first tick, so restarts do not hammer upstream. A failed
  refresh is logged and retried next tick rather than taking the server down — stale data
  beats no data.
- `GET /healthz` returns 200 only once the cache actually has data, so a rollout does not
  go green on an instance that can serve nothing but the model view.

The UI shows the mode and the refresh cadence in its header. The parsed graph is cached in
memory and reloaded only when the cache's timestamp changes, so a refresh — scheduled, or
from a separate `vkstack refresh` against the same cache — is picked up without a restart.

There is no authentication. The data is public, but bind accordingly.

## Two things worth knowing

**Three of the ten product pairs have no upstream data.** ESX × VKS, ESX × VKr and
**Supervisor × VKr** return nothing at all. So "compatible", "incompatible" and "no data
published" are kept distinct everywhere — a stack is never reported as verified on a pair
that was never published. The Supervisor × VKr gap is the one that matters, and it is
inferred through vCenter and VKS.

A missing pair and a missing *cell* are different things. Inside a pair upstream does
publish, two releases with no result between them are not an open question — that is the
matrix declining to list them together — so they cannot appear in one stack, whether the
solver picked them or you pinned them. Pinning two such releases reports that they are
never listed together, rather than solving a stack around a combination nobody published.

**Only vCenter and ESX 8.0 U3 and later are in scope.** Supervisor, VKS and VKr have no
hardcoded floor; they are filtered by reachability instead, so anything that only ever
worked with vSphere 7 drops out on its own. `--all-versions` disables the floor and
`--min-version vcenter=9.0.0.0` moves it. The floor is applied when the cache is read, not
when it is written, so changing it never needs a refetch.

## Layout

```
cmd/vkstack/        entry point
internal/model/     the conceptual dependency model; emits the mermaid diagram
internal/version/   two version schemes (see below)
internal/api/       client for the JSON API behind the interop SPA
internal/store/     SQLite cache — a dumb mirror of what upstream returned
internal/graph/     in-memory queries: compat, check, stack solving
internal/cli/       cobra commands; output.go holds the agent-facing contract,
                    describe.go the surface document, mcp.go the stdio MCP server
internal/web/       localhost UI, assets embedded
docs/model.md       generated from internal/model — do not edit by hand
AGENTS.md           the contract for programs calling this tool
```

`docs/model.md` is generated. Regenerate with:

```sh
go test ./internal/model -update
```

A test fails if the committed copy drifts from the model.

### Version parsing

Two schemes, never compared against each other:

- **vSphere** (ESX, vCenter) mixes forms across majors — `8.0U3k` for 8.x, `9.1.0.0300`
  for 9.x — and ESX writes `8.0c` where vCenter writes `8.0.0c` for the same release. A
  generic dotted-number parser sorts `8.0c` *above* `8.0U3`, which is wrong, so this
  scheme normalises to `[major, minor, patch, update, letter, build]`.
- **generic** (Supervisor, VKS, VKr) splits into numeric runs and compares
  lexicographically, which is exactly right for `v1.33.9+vmware.3-fips-vsc0.1.15`.

## Development

```sh
go test ./...

# Check the recorded pair coverage still matches upstream (needs a refreshed cache):
VKSTACK_TEST_CACHE=~/.cache/vkstack/vkstack.db go test ./internal/graph -run Coverage
```

The upstream auth key is public — it is a literal in the interop SPA's JavaScript bundle.
If it rotates, `refresh` re-derives both the key and the service URL from the live bundle
on a 401/403 rather than failing.
