# vkstack

Compatibility across vCenter, ESX, vSphere Supervisor, VKS and VKr — plus optional NSX
and Avi Load Balancer — from the Broadcom Product Interoperability Matrix.

> **Unofficial.** Not affiliated with or endorsed by Broadcom. Broadcom publishes no
> public API for the interoperability matrix, so this calls the same private JSON
> endpoint the matrix website's own front end calls, with the auth key that site ships
> in its public JavaScript. There is no login, and none of your credentials are involved.
> That key is never bundled into this tool or written to disk: each `refresh` derives it
> from the live site and then discards it. The endpoint is undocumented, so it can change
> or stop working without notice. The matrix itself is the authority, so verify anything
> you are relying on at <https://interopmatrix.broadcom.com>.

The matrix answers one question at a time: is A compatible with B. This answers the
question people usually have instead, which is what the whole valid stack looks like.

A single Go binary, no cgo, no runtime dependencies. Data is cached locally, so
everything after the first `refresh` works offline.

## Two surfaces, two audiences

People use the web UI and agents use the CLI. That split is deliberate, and it decides
how each side is built.

Someone asking "what can I run with vCenter 8.0U3k" is served far better by a map they
can click through than by a table, so `serve` gets the interaction, the provenance
shading and the support-lifecycle colouring. A program asking the same question wants
data it does not have to scrape, so every CLI read command emits a versioned JSON
envelope by default, errors are JSON with stable codes, and exit codes separate a bad
question from a well-formed one whose answer is no. `--human` brings the tables back.

For agents there is also `vkstack describe`, which states the whole surface as JSON, and
`vkstack mcp`, which serves the same queries as MCP tools over stdio. See
[AGENTS.md](AGENTS.md).

## Quick start

```sh
curl -fsSL https://raw.githubusercontent.com/warroyo/vkstack/main/install.sh | sh
```

One static binary, no toolchain and no runtime dependencies. It lands in
`/usr/local/bin` if you own that, otherwise `~/.local/bin`, and every download is verified
against the release's own `checksums.txt` before anything is put on your PATH.

```sh
vkstack refresh                    # pull the matrix into a local cache (~1 min)
vkstack serve --open               # the map, for a person
vkstack stack vcenter 8.0U3k       # the whole valid stack, as JSON
vkstack stack vcenter 8.0U3k --human
```

Pin a version or pick the destination:

```sh
VKSTACK_VERSION=v0.1.0 VKSTACK_INSTALL_DIR=~/bin \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/warroyo/vkstack/main/install.sh)"
```

Piping a script from the internet into a shell is worth a second thought. The script is
short and does nothing clever, so read it first if you would rather:

```sh
curl -fsSL -O https://raw.githubusercontent.com/warroyo/vkstack/main/install.sh
less install.sh && sh install.sh
```

Other ways in: `npm i -g @warroyo90/vkstack-mcp`, which carries the same prebuilt binaries;
grab an archive from the [releases page](https://github.com/warroyo/vkstack/releases)
(Linux, macOS and Windows, amd64 and arm64);
`go install github.com/warroyo/vkstack/cmd/vkstack@latest` if you have Go; or
`go build ./cmd/vkstack` from a clone.

## Wiring it into an agent

Nothing to install first — this is the whole setup, in any client that reads an
`mcpServers` object:

```json
{
  "mcpServers": {
    "vkstack": {
      "command": "npx",
      "args": ["-y", "@warroyo90/vkstack-mcp", "mcp", "--refresh-if-empty"]
    }
  }
}
```

Claude Code:

```sh
claude mcp add vkstack -- npx -y @warroyo90/vkstack-mcp mcp --refresh-if-empty
```

`npx` pulls the `@warroyo90/vkstack-mcp` package, which declares one prebuilt binary per
platform as an optional dependency, so a machine downloads only its own.
`--refresh-if-empty` fills the cache on first start, which is what makes a cold machine
answer at all; it costs about a minute, once. [AGENTS.md](AGENTS.md) has the rest: the
tools, the envelope, the exit codes, and the same config for a machine that already has
the binary.

## Commands

| Command | What it does |
|---|---|
| `describe` | The whole agent-facing surface (commands, schemas, exit codes) as JSON |
| `explain` | The dependency model (JSON; `--human` for prose, `--ascii` for a bare terminal) |
| `refresh` | Pull the matrix into `~/.cache/vkstack/vkstack.db` |
| `stack <product> <version>` | Solve a whole valid stack from one or more pinned versions (`--with nsx,avi` to include the optional components) |
| `compat <product> <version>` | The raw pairwise answer for one release |
| `check --vcenter … --esx …` | Validate a fully pinned stack; exit 6 if incompatible |
| `products` / `releases` | What is in scope, and which pairs upstream publishes |
| `serve` | Web UI: the stack map, local by default or hosted (see below) |
| `static` | Generate the UI as plain files, for a static host (see below) |
| `mcp` | Serve the queries as MCP tools over stdio, for an agent |
| `cache info\|path\|clear` | Inspect or drop the cache |

Output is JSON unless you ask otherwise: `--human` for tables and prose, `--csv` on the
commands whose answer really is rows (`releases`, `compat`, `products`), or
`VKSTACK_OUTPUT=human` to set a default for your shell. Product keys are `vcenter`,
`esx`, `supervisor`, `vks`, `vkr`, and the two optional ones, `nsx` and `avi`.

## The stack map

`vkstack serve` opens on a layered map of the whole stack, drawn bottom-up: vCenter at
the base, branching up through Supervisor, VKS and VKr. Pick any version at any layer and
the map redraws around it, with a list underneath showing every version in every layer,
lit or faded.

Only real dependencies constrain a stack. The chain is:

```
VKr  →  VKS  →  Supervisor  →  vCenter  ↔  ESX
```

NSX and Avi hang off that chain rather than sitting in it, and each is opted into on its
own:

```
        NSX  ─┐
              ├→  Supervisor
        Avi  ─┘
```

Both are optional, and neither implies the other. They start as collapsed rows; open one
and it becomes a layer like any other, with its own versions to pick from. Opening NSX
leaves Avi closed, and Avi in front of a distributed switch with no NSX anywhere is an
ordinary deployment.

The matrix also publishes vCenter against VKS, vCenter against VKr, and ESX against Avi.
Those are worth looking up, but they are not dependencies, because VKS runs on the
Supervisor, VKr is provisioned by VKS, and Avi's service engines are placed through
vCenter — so they are reported and never enforced. Enforcing them produces combinations
that are listed compatible yet cannot exist: vCenter 9.0.0.0 and VKS 3.7 are listed
together, but vCenter 9.0.0.0 tops out at Supervisor 1.30 while VKS 3.7 needs 1.32, so
there is no Supervisor to put in the middle. ESX × Avi fails the other way — the grid is
so nearly empty that enforcing it would leave one legal combination.

A node is lit when a complete valid stack exists containing it and your selection,
enforcing the chain above. `vkstack check` reports the non-dependency pairs separately,
under "not part of the verdict".

A node usually stands for several releases, and clicking it selects the newest. The peek
on hover says which one, and lists the rest. Where the matrix hedges, the picture hedges
with it: a dashed connector means upstream lists the combination compatible without
having tested it, and a dotted one means it published no result for that pair and the
link is inferred from the rest of the stack. Upstream footnotes on a result, such as
"Running Supervisor is compatible after VC upgrade", are shown rather than dropped.

Four things are grouped deliberately.

- Supervisor is split by release train. The same Kubernetes version ships on two
  trains that are *not* interchangeable: `vsc9.x` ships with vCenter 9.x, `vsc0.x` is
  versioned independently. Supervisor 1.31 on vsc9 will not run on a vCenter 8
  deployment. Each train is its own node, badged `vsc9` / `vsc0`, so the two never read
  as one version. Which trains a vCenter accepts comes from the matrix rather than from
  a rule of ours: vCenter 9.1.0.0300 accepts both.
- vCenter is not collapsed by patch. 8.0U3 supports Supervisor 1.26–1.28 while 8.0U3k
  supports 1.31–1.33, so hiding the patch letter would throw away the answer.
- ESX is not a layer. Its release lines are identical to vCenter's and it has no
  published data against VKS or VKr, so it appears as "on ESX 9.1 · 9.0 · 8.0U3" under
  each vCenter node instead of a row nobody can branch from.
- NSX and Avi are grouped by major.minor line, since neither carries a Kubernetes
  version to group on: NSX `9.1` covers 9.1.0.0 through 9.1.0.0200, Avi `32.1` covers
  32.1.1 and 32.1.2.

### Support lifecycle

The matrix publishes a support phase on every release, and the map colours it:

| | |
|---|---|
| General Support | normal |
| Technical Guidance | amber, tagged `TG`; General Support has ended, no new fixes |
| End of Support | red and struck through, tagged `EOS` |

"Legacy" means the same thing here as on the interop site: nothing left in General
Support. The site's *hide legacy releases* checkbox maps onto the same two flags this
uses (`isHideGenSupported` and `isHideTechSupported`), so the **Hide legacy releases**
toggle mirrors it, on by default. On the CLI, `vkstack releases <product> --legacy`
includes them.

A grouped node keeps the best phase among its releases as its headline, because the line
really is still usable, but it names its worst too, so a green node covering an End of
Support build cannot be picked from blind.

The toggle filters the *picture*, not the solver. So "Newest working stack from here" can
name a release the map is hiding; it carries the phase of every release in it and says
when it reached past the filter, rather than reading as though everything shown is in
General Support.

Releases upstream has not published anything for yet, such as 9.2.0.0 at the time of
writing, are marked "no data yet" rather than silently omitted.

## Hosting a shared instance

By default `serve` binds `127.0.0.1` and only refreshes when asked, which is what you want
on a laptop. For a shared instance, bind wider, make it read-only, and let the server keep
itself current:

```sh
vkstack serve --bind 0.0.0.0 --port 8080 --read-only --refresh-interval 6h
```

- `--read-only` rejects client-triggered refreshes with a 403, so visitors cannot make the
  server call upstream. Everything else still works: it is read-only to *clients*, while
  the scheduled refresh keeps writing to the cache.
- `--refresh-interval` sets the cadence at runtime. A cold cache refreshes immediately on
  start; a warm one waits for the first tick, so restarts do not hammer upstream. A failed
  refresh is logged and retried next tick rather than taking the server down, so stale
  data is still served.
- `GET /healthz` returns 200 only once the cache actually has data, so a rollout does not
  go green on an instance that has nothing to answer with yet.

The UI shows the mode and the refresh cadence in its header. The parsed graph is cached in
memory and reloaded only when the cache's timestamp changes, so a refresh, whether
scheduled or from a separate `vkstack refresh` against the same cache, is picked up
without a restart.

There is no authentication. The data is public, but bind accordingly.

### In a container

Images are published to `ghcr.io/warroyo/vkstack` for `linux/amd64` and `linux/arm64`.
Tags: a version (`v0.1.0`, `0.1`), `latest` for the newest release, `edge` for `main`.

```sh
docker run --rm -p 8080:8080 ghcr.io/warroyo/vkstack:latest
```

The default command is the hosted one above, so that starts read-only, refreshes every
six hours, and fetches the matrix immediately because the cache is cold. The CLI is in
the same image, so any other subcommand works too:

```sh
docker run --rm ghcr.io/warroyo/vkstack:latest stack vcenter 8.0U3k
```

The cache lives at `/var/cache/vkstack`. Mount a volume there to keep it across
restarts; without one, each start refetches, which takes about a minute. The image is
distroless and runs as uid 65532 with no shell in it.

### On Kubernetes

`deploy/k8s` is a Deployment and a ClusterIP Service, and nothing else:

```sh
kubectl apply -k deploy/k8s
kubectl port-forward svc/vkstack 8080:80
```

It runs read-only with a six-hour refresh, caches to an `emptyDir`, and gates readiness
on `/healthz`, so a pod does not take traffic until it has data. Liveness is deliberately
a different probe: an upstream outage should not restart a process that is serving
perfectly good cached data.

One replica is intentional. Each pod holds its own SQLite cache and refreshes on its own,
so replicas mean several callers upstream and several slightly different pictures.

To pin a version, or to build your own layer on top:

```sh
cd deploy/k8s && kustomize edit set image ghcr.io/warroyo/vkstack:v0.1.0
```

```yaml
resources:
  - github.com/warroyo/vkstack/deploy/k8s?ref=v0.1.0
```

There is still no authentication, so put an Ingress in front of it accordingly.

### As a static site

`vkstack static` writes the UI as plain files, with no server behind it:

```sh
vkstack refresh
vkstack static --out dist
```

Publish `dist` anywhere that serves files. <https://vkstack.warroyo.com> is this,
republished from GitHub Actions on every push to `main`, on every release, and nightly to
pick up upstream. The page footer names the build that generated it.

This works because the map pins one version at a time, so the questions it can ask are
finite: the build drives all 65 of them — the unpinned map plus one per clickable node —
through the same handlers `serve` uses and ships the answers alongside the page. The
generated site and a hosted one cannot disagree, because the same code produced both.
Selecting a version becomes a lookup rather than a round trip.

Opening NSX or Avi changes every answer on the map, not a corner of it, so each
combination of open optional layers gets its own bundle — `data.json`, `data-nsx.json`,
`data-avi.json`, `data-nsx-avi.json` — and the page fetches only the one it is showing.
Most readers run neither, and they should not download the other three states to find
that out.

What it does not carry is the rest of the tool. Multi-pin solves (`stack --vcenter …
--vks …`) and `check` span combinations that cannot be enumerated ahead of time, and they
stay where they were: the CLI, the HTTP API and MCP. A static site is the sample for
people, not the surface for programs.

Being a snapshot, it is current only as of its last build. Rebuild to update it; there is
nothing running that could refresh itself.

## Two things worth knowing

NSX and Avi are optional, and independent of each other. Neither appears in a solved
stack unless you pin it or ask for it, because neither is in every deployment: a
Supervisor runs on NSX networking, or on a vSphere Distributed Switch with Avi in front
of it, or on neither. Asking for one never brings in the other — **Avi does not require
NSX** — and a stack that says nothing about NSX is a complete answer about the five core
components, not a claim that you have no NSX.

```sh
vkstack stack vcenter 9.1.0.0300 --with nsx        # NSX, no Avi
vkstack stack vcenter 9.1.0.0300 --with avi        # Avi on VDS, no NSX
vkstack stack vcenter 9.1.0.0300 --with nsx,avi    # both
vkstack stack --avi 32.1.2                         # a pin is its own opt-in
```

In the web UI they are two collapsed rows you open one at a time.

Seven of the twenty-one product pairs have no upstream data. ESX × VKS, ESX × VKr,
Supervisor × VKr, and NSX and Avi each against VKS and VKr return nothing at all. So
"compatible", "incompatible" and "no data published" are kept distinct everywhere, and a
stack is never reported as verified on a pair that was never published. The
Supervisor × VKr gap is the one that matters, and it is inferred through vCenter and VKS.

One published pair is deliberately never enforced: ESX × Avi exists upstream but is
nearly empty — three cells in the whole grid say yes. Avi's service engines are placed
through vCenter, so vCenter is the pair that decides. Enforcing ESX × Avi would rule out
Avi deployments that plainly work, so it is reported and never used to include or exclude.

A missing pair and a missing *cell* are different things. Inside a pair upstream does
publish, two releases with no result between them are not an open question. That is the
matrix declining to list them together, so they cannot appear in one stack, whether the
solver picked them or you pinned them. Pinning two such releases reports that they are
never listed together, rather than solving a stack around a combination nobody published.

Only vCenter and ESX 8.0 U3 and later are in scope. Supervisor, VKS, VKr, NSX and Avi
have no hardcoded floor; they are filtered by reachability instead, so anything that only
ever worked with vSphere 7 drops out on its own — which is what retires the older NSX and
Avi lines without inventing a cutoff. `--all-versions` disables the floor and
`--min-version vcenter=9.0.0.0` moves it. The floor is applied when the cache is read, not
when it is written, so changing it never needs a refetch.

`--generation 9` narrows to one vSphere platform generation, and the web UI offers the
same choice as a tab. A generation names a **vCenter major version and constrains nothing
else**. That is not a simplification: every other component crosses the line in the
published data. ESX 8.x pairs with vCenter 9 in the hundreds of rows, NSX 4.x has more
compatible vCenter 9 pairs than NSX 9.x does, the Supervisor's `vsc0` train serves both,
and Avi 31.x spans them. So a generation filters vCenter, and everything else is kept or
dropped by whether it can still reach a surviving vCenter — which means a component that
genuinely works with both appears under both, because it does.

## Layout

```
cmd/vkstack/        entry point
internal/model/     the conceptual dependency model; emits the mermaid diagram
                    used by `graph` and docs/model.md
internal/version/   two version schemes (see below)
internal/api/       client for the JSON API behind the interop SPA
internal/store/     SQLite cache — a dumb mirror of what upstream returned
internal/graph/     in-memory queries: compat, check, stack solving
internal/cli/       cobra commands; output.go holds the agent-facing contract,
                    describe.go the surface document, mcp.go the stdio MCP server
internal/web/       localhost UI, assets embedded
npm/                the npm face: bin/vkstack.js launches the right prebuilt
                    binary, build.mjs stages the packages from a goreleaser build
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

- vSphere (ESX, vCenter) mixes forms across majors, `8.0U3k` for 8.x and `9.1.0.0300`
  for 9.x, and ESX writes `8.0c` where vCenter writes `8.0.0c` for the same release. A
  generic dotted-number parser sorts `8.0c` *above* `8.0U3`, which is wrong, so this
  scheme normalises to `[major, minor, patch, update, letter, build]`.
- generic (Supervisor, VKS, VKr) splits into numeric runs and compares
  lexicographically, which is exactly right for `v1.33.9+vmware.3-fips-vsc0.1.15`.

## Development

```sh
go test ./...

# Check the recorded pair coverage still matches upstream (needs a refreshed cache):
VKSTACK_TEST_CACHE=~/.cache/vkstack/vkstack.db go test ./internal/graph -run Coverage
```

### The npm packages

Publishing happens in the release workflow; staging them locally is how you check the
launcher before a tag goes out. `build.mjs` reads goreleaser's own manifest rather than
building anything, so it needs a `dist/` to read:

```sh
goreleaser build --snapshot --clean
node npm/build.mjs 0.0.0-test
```

It ends by running the staged launcher against the staged package for this machine, so a
broken resolve or a lost permission bit fails there rather than on the registry.

### The auth key

The upstream auth key is public, a literal in the interop SPA's JavaScript bundle, but
it is deliberately not checked into this repo and not cached to disk. Every `refresh`
fetches the SPA shell, finds the `main.<hash>.js` it references, and pulls both the key
and the service URL out of it (`internal/api/rediscover.go`). That is two extra requests
per refresh, against twenty-two that were happening anyway, and a rotation between runs is
invisible. A rotation *during* a run costs one retry: any 401/403 discards the key,
re-derives it and repeats the call once.

`--auth-key` skips discovery entirely. It is the escape hatch for the case discovery
cannot handle, the SPA changing shape so the regexes stop matching, so a key passed
that way is never replaced and its rejection is reported as-is. `internal/api` tests run
those regexes against a fixture bundle in `testdata/`, so rot fails CI rather than the
field. That fixture's key is fake, on purpose.
