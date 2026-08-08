# interop

Compatibility and upgrade planning across **vCenter, ESX, vSphere Supervisor, VKS and
VKr**, from the Broadcom Product Interoperability Matrix.

The matrix answers one question at a time — "is A compatible with B". This answers the
questions people actually have: *what is the whole valid stack?* and *what order do I
upgrade in?*

Single Go binary, no cgo, no runtime dependencies. Data is cached locally, so everything
after the first `refresh` works offline.

## Quick start

```sh
go build ./cmd/interop

./interop explain                    # how the pieces fit together
./interop refresh                    # pull the matrix into a local cache (~1 min)
./interop stack vcenter 8.0U3k       # the whole valid stack from one pinned version
./interop serve --open               # the same thing, in a browser
```

## Commands

| Command | What it does |
|---|---|
| `explain` | The dependency model as mermaid (`--ascii` for a bare terminal) |
| `refresh` | Pull the matrix into `~/.cache/interop/interop.db` |
| `stack <product> <version>` | Solve a whole valid stack from one or more pinned versions |
| `compat <product> <version>` | The raw pairwise answer for one release |
| `check --vcenter … --esx …` | Validate a fully pinned stack; exits non-zero if incompatible |
| `upgrade --from … --to …` | Ordered upgrade steps, grouped into maintenance windows |
| `products` / `releases` | What is in scope, and which pairs upstream publishes |
| `serve` | Web UI — the stack map, local by default or hosted (see below) |
| `cache info\|path\|clear` | Inspect or drop the cache |

`--json` and `--csv` work on every read command. Product keys are `vcenter`, `esx`,
`supervisor`, `vks`, `vkr`.

## The stack map

`interop serve` opens on a layered map of the whole stack, drawn bottom-up: vCenter at
the base, branching up through Supervisor, VKS and VKr. Pick any version at any layer and
the map redraws around it, with a list underneath showing every version in every layer,
lit or faded.

**The matrix is the source of truth.** If it lists two versions as compatible, the map
shows that — always. A node is lit whenever the matrix lists it against your selection.

Connectors answer a stronger question: can you build a whole stack around both? Some
pairs are listed compatible with nothing to bridge them — vCenter 9.0.0.0 and VKS 3.7 are
listed together, but no Supervisor works with both. Those nodes stay lit and are marked
"no full stack" with no connector, rather than being hidden.

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

Releases upstream has not published anything for yet — 9.2.0.0 at the time of writing —
are marked "no data yet" rather than silently omitted.

## Hosting a shared instance

By default `serve` binds `127.0.0.1` and only refreshes when asked, which is what you want
on a laptop. For a shared instance, bind wider, make it read-only, and let the server keep
itself current:

```sh
interop serve --bind 0.0.0.0 --port 8080 --read-only --refresh-interval 6h
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
from a separate `interop refresh` against the same cache — is picked up without a restart.

There is no authentication. The data is public, but bind accordingly.

## Three things worth knowing

**Three of the ten product pairs have no upstream data.** ESX × VKS, ESX × VKr and
**Supervisor × VKr** return nothing at all. So "compatible", "incompatible" and "no data
published" are kept distinct everywhere — a stack is never reported as verified on a pair
that was never published. The Supervisor × VKr gap is the one that matters, and it is
inferred through vCenter and VKS.

**Upgrade paths are derived here, not taken from upstream.** The interop API has an
`/upgrades` endpoint; its data is not reliable enough to build on. Plans instead come from
the release list plus the compatibility matrix under three rules: forward-only, the
Kubernetes minor advances by at most one per step, and the final stack must be supported.

Intermediate stacks are *allowed* to be unsupported — they have to be, since vCenter
cannot move without transiently breaking its pairing with the Supervisor it manages. Runs
of unsupported states are grouped into **maintenance windows**: everything in a window has
to finish before you stop.

**Only vCenter and ESX 8.0 U3 and later are in scope.** Supervisor, VKS and VKr have no
hardcoded floor; they are filtered by reachability instead, so anything that only ever
worked with vSphere 7 drops out on its own. `--all-versions` disables the floor and
`--min-version vcenter=9.0.0.0` moves it. The floor is applied when the cache is read, not
when it is written, so changing it never needs a refetch.

## Layout

```
cmd/interop/        entry point
internal/model/     the conceptual dependency model; emits the mermaid diagram
internal/version/   two version schemes (see below)
internal/api/       client for the JSON API behind the interop SPA
internal/store/     SQLite cache — a dumb mirror of what upstream returned
internal/graph/     in-memory queries: compat, check, stack solving, upgrade planning
internal/cli/       cobra commands
internal/web/       localhost UI, assets embedded
docs/model.md       generated from internal/model — do not edit by hand
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
INTEROP_TEST_CACHE=~/.cache/interop/interop.db go test ./internal/graph -run Coverage
```

The upstream auth key is public — it is a literal in the interop SPA's JavaScript bundle.
If it rotates, `refresh` re-derives both the key and the service URL from the live bundle
on a 401/403 rather than failing.
