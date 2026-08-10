# vkstack, for agents

This CLI is built to be called by programs. The web UI (`vkstack serve`) is the surface
for people; everything here is the surface for everything else.

Two ways in:

- **Shell out** to the CLI. Every read command emits a versioned JSON envelope on stdout.
- **Speak MCP** to `vkstack mcp`, and call typed tools instead of parsing anything.

Both answer from a local cache and make no network calls. Only `vkstack refresh` reaches
upstream, so no question an agent asks can cause outbound traffic.

## Start here

```sh
vkstack describe
```

That returns this document's contract as data: every command, its arguments, the schema
it returns, and what each exit code means. It needs no cache and no network. Read it
before hardcoding anything — it is generated from the same source as the behaviour, so it
cannot drift the way prose can.

## The envelope

Every JSON-emitting command returns one object with the same shape:

```json
{
  "schema": "vkstack.stack",
  "version": 1,
  "tool": "vkstack/1.2.0",
  "snapshot": { "fetchedAt": "2026-08-08T13:15:54-06:00", "ageHours": 11, "stale": false },
  "data": { }
}
```

- `schema` names the payload. Branch on it.
- `version` is bumped only on a breaking change to `data`. New fields are additive and do
  not bump it, so parse permissively.
- `snapshot` says which cache the answer came from. **A compatibility answer is only as
  current as the data behind it** — if `stale` is true, say so rather than presenting the
  answer as current, and suggest `vkstack refresh`.
- `data` is the payload, shaped per command.

## Errors are data

Failures print an `vkstack.error` document **on stderr** and write nothing to stdout, so
stdout is always either one valid envelope or empty. No success check needed before
parsing.

```json
{
  "schema": "vkstack.error",
  "version": 1,
  "error": {
    "code": "release_not_found",
    "message": "no vCenter release matches \"99.9\" (newest available: 9.2.0.0)",
    "details": { "product": "vCenter", "input": "99.9", "newest": "9.2.0.0" },
    "hint": "list what exists with `vkstack releases <product>`"
  }
}
```

Branch on `code`. `hint` names the next thing to try, so recovery does not need guessing.

## Exit codes

| Code | Name | Means |
|---|---|---|
| 0 | ok | succeeded |
| 1 | error | something went wrong |
| 2 | usage | called wrongly |
| 3 | not_found | no such product or release |
| 4 | ambiguous | the version matched several releases — pass one verbatim |
| 5 | no_data | the cache is empty; run `vkstack refresh` |
| 6 | incompatible | **the question was valid and the answer is no** |
| 7 | no_stack | no complete stack exists for those pins |

6 and 7 are answers, not malfunctions. `check` writes its full verdict to stdout and
*then* exits 6 — read the payload, do not treat the exit code alone as a failure.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/warroyo/vkstack/main/install.sh | sh
```

One static binary, no toolchain required. It goes to `/usr/local/bin` when writable and
`~/.local/bin` otherwise; set `VKSTACK_INSTALL_DIR` to choose, `VKSTACK_VERSION` to pin.
Each download is checked against the release's `checksums.txt` before install.

If a Go toolchain is present, `go install github.com/warroyo/vkstack/cmd/vkstack@latest`
works too, as does downloading an archive from the
[releases page](https://github.com/warroyo/vkstack/releases), or npm:

```sh
npm install -g @warroyo90/vkstack-mcp     # puts `vkstack` on PATH
npx -y @warroyo90/vkstack-mcp --version   # or no install at all
```

To wire it into an agent without installing anything, skip to [MCP](#mcp).

Then populate the cache once — **this step is not optional**, and skipping it is the most
likely reason a fresh install appears broken:

```sh
vkstack refresh        # ~1 min, ~40MB, the only command that touches the network
vkstack cache info     # confirm: data.empty == false
```

`vkstack cache info` is the preflight. It returns `empty` and `stale` as JSON, so a caller
can check readiness before asking a real question instead of discovering the problem
through a failed answer.

## MCP

### Nothing installed: npx

The published [`@warroyo90/vkstack-mcp`][npm] package carries the same prebuilt binaries as
the release archives, one per platform, and npm installs only the one matching the
machine. So a config block is enough on its own — there is nothing to install first:

[npm]: https://www.npmjs.com/package/@warroyo90/vkstack-mcp

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

That is the shape Claude Desktop, Cursor, Windsurf, Codex, Continue and anything else
reading an `mcpServers` object will take.

**Claude Code:**

```sh
claude mcp add vkstack -- npx -y @warroyo90/vkstack-mcp mcp --refresh-if-empty
```

`--refresh-if-empty` is not optional here the way it is below. A machine that reaches for
`npx` is by definition one nobody prepared, so its cache is empty and every tool call
would fail without it. The cost lands on the first call after the server starts, which
spends ~1 min fetching before it answers; everything after that is local. Run
`npx -y @warroyo90/vkstack-mcp refresh` once by hand beforehand if you would rather not pay
it inside a conversation.

The package ships the whole CLI, not just the server:
`npx -y @warroyo90/vkstack-mcp stack vcenter 8.0U3k` works too, and a global install
(`npm i -g @warroyo90/vkstack-mcp`) puts `vkstack` on PATH.

### Already have the binary

```json
{
  "mcpServers": {
    "vkstack": {
      "command": "vkstack",
      "args": ["mcp"]
    }
  }
}
```

Claude Code: `claude mcp add vkstack -- vkstack mcp`. This repo also ships a
project-scoped [`.mcp.json`](.mcp.json) in this form, so an agent working inside a clone
gets the server offered with no setup at all.

Add `--refresh-if-empty` here too if the cache may not be populated yet:

```json
{ "mcpServers": { "vkstack": { "command": "vkstack", "args": ["mcp", "--refresh-if-empty"] } } }
```

That flag is deliberately a launch option rather than lazy behaviour inside a tool call:
the operator starting the server decides whether the host may reach upstream, so a model
asking a question can never trigger network I/O as a side effect. A stale cache is left
alone — only a completely empty one is filled.

### What the server gives you

Tools: `vkstack_stack`, `vkstack_check`, `vkstack_compat`, `vkstack_releases`,
`vkstack_products`, `vkstack_model`. Each returns both a text block and
`structuredContent`, so a client that supports structured results need not re-parse.

`vkstack_stack` takes an optional `include` array naming the optional products to put in
the stack — `["nsx"]`, `["avi"]`, or `["nsx", "avi"]`. Each stands alone; omit it for the
five core products only. `vkstack_products` marks which products are `optional`, so the
valid values are discoverable rather than something to hardcode.

A failed call comes back as a tool result with `isError: true` and the reason in text,
not as a protocol error — the model should be able to see what went wrong and retry.

The transport is stdio: each agent runs its own process, nothing listens on a port, and
access to the process is the whole authorization boundary. There is no HTTP transport and
no auth layer, so do not put this behind a network proxy without adding both.

## What to know before reasoning about the answers

Call `vkstack explain` (schema `vkstack.model`) once and keep it in context. It carries
the six things most likely to produce a confidently wrong conclusion:

1. **Only real dependencies constrain a stack.** The chain is
   `VKr → VKS → Supervisor → vCenter ↔ ESX`. The matrix *also* publishes vCenter against
   VKS, vCenter against VKr, and ESX against Avi, and those are **not** dependencies —
   VKS runs on the Supervisor, VKr is provisioned by VKS, Avi is placed through vCenter.
   They are reported, never enforced. Enforcing them produces combinations listed as
   compatible that cannot exist, and rules out combinations that work.
2. **NSX and Avi are optional, and independent of each other.** Neither is in a solved
   stack unless it is pinned or named in `stack --with` / the MCP `include` array. All
   four combinations are real deployments: neither, NSX alone, **Avi alone** (a
   Supervisor on a distributed switch with Avi in front of it), or both. Asking for one
   never brings in the other, and **Avi does not require NSX**. A stack that does not
   mention NSX is a complete answer about the five core products — not a claim that the
   deployment has no NSX.
3. **ESX × Avi is published and almost entirely blank.** At the time of writing three
   cells in the whole grid say yes, all of them Avi 32.1.1 against ESX 9.1.x. It is not a
   dependency and is never enforced. If you enforce it yourself you will reject Avi
   deployments that plainly work.
4. **Seven of the twenty-one product pairs have no upstream data at all** (ESX×VKS,
   ESX×VKr, Supervisor×VKr, NSX×VKS, NSX×VKr, Avi×VKS, Avi×VKr). "No data published" is
   not "incompatible", and the tool never reports a stack as verified on a pair nobody
   published. Do not fill that gap with a guess.
5. **A missing cell is a no.** Inside a pair upstream *does* publish, two releases with no
   result between them are the matrix declining to list them together — not an open
   question.
6. **"Compatible, not tested" is a real distinction.** Status 3 counts as a yes when
   solving, and is always reported as its own state. Pass it on rather than flattening it.

`data.unknowns` in `vkstack.model` lists every limit in the tool's own words, including
the big one: **the matrix says which versions may coexist, not whether a hop is supported
as an upgrade**. A stack this tool calls valid is a valid *destination*, not a route.

## Recipes

```sh
# The whole stack from one pinned version
vkstack stack vcenter 8.0U3k

# Same, allowing patch releases
vkstack stack vcenter 8.0U3k --patches

# Pin several layers at once
vkstack stack vcenter 8.0U3k supervisor v1.32.9

# Optional components. Each is opted into on its own; Avi does not require NSX.
vkstack stack vcenter 9.1.0.0300 --with nsx
vkstack stack vcenter 9.1.0.0300 --with avi
vkstack stack vcenter 9.1.0.0300 --with nsx,avi

# Pinning an optional component is its own opt-in
vkstack stack --avi 32.1.2

# Validate a stack you already have; exit 6 means incompatible
vkstack check --vcenter 8.0U3k --esx 8.0U3 --supervisor v1.33.9+vmware.3-fips-vsc0.1.15

# What exists, before asking about a version that does not
vkstack releases supervisor --patches

# Is the cache present and fresh enough to trust?
vkstack cache info
```

Version strings resolve by exact match first, then unique prefix. A prefix matching
several releases is an error (exit 4) with the candidates in `details`, never a silent
pick — so `9.1` is rejected rather than guessed at.

## Conventions worth honouring

- Do not parse `--human` output. It is prose for people and may change freely.
- Do not scrape `--help`. `vkstack describe` is the contract.
- Quote the exact release strings back to the user; they are what the matrix publishes and
  what any other tool will expect.
- When reporting an answer onward, carry `snapshot.fetchedAt` with it. A compatibility
  claim without a date is not a claim anyone can check.
