#!/usr/bin/env python3
"""
Validate the UI's drawn connectors against the live Broadcom interoperability matrix.

Every edge the stack map draws carries the two exact releases it is about
(mapEdge.Releases). This script harvests every such edge from a generated static site
(all pinned views, all optional subsets, all generations), then for each distinct
(productA, releaseA, productB, releaseB) re-queries the *live* matrix for those two
releases and checks that what the UI drew matches what upstream actually publishes.

Invariants checked, per drawn edge:
  * a solid/untested edge MUST land on a compatible cell (matrix status 1 or 3);
    a line drawn on a missing or NOT-SUPPORTED (status 4) cell is a UI bug.
  * an `untested` edge MUST be status 3 (compatible, not tested), and a plain edge
    status 1 (compatible).
  * an `unverified` edge MUST have no cell for those two releases (drawn on trust,
    not on a lookup).

Usage:  python3 scripts/validate-ui.py [-v] <static-site-dir>

Generate the site first (`vkstack static --out dist`) against a freshly refreshed cache,
so the UI and the live matrix describe the same snapshot. The auth key and service URL are
discovered from the public SPA bundle — no secret is needed. Exits non-zero on any
mismatch, so it can gate a scheduled CI job.

PRODUCT_ID below mirrors internal/model.Products; keep it in step if a product is added.
"""
import json, os, sys, glob, subprocess, urllib.request

PRODUCT_ID = {  # model product key -> upstream interop product id
    "vcenter": 2, "esx": 1, "nsx": 912, "avi": 1795,
    "supervisor": 1378, "vks": 1794, "vkr": 820, "tmc": 1771,
}
STATUS = {1: "compatible", 2: "incompatible", 3: "compatible-not-tested", 4: "not-supported"}


def discover():
    kf, sf = "/tmp/vk_key", "/tmp/vk_svc"
    if os.path.exists(kf) and os.path.exists(sf) and os.path.getsize(kf):
        return open(kf).read().strip(), open(sf).read().strip()
    page = urllib.request.urlopen("https://interopmatrix.broadcom.com/Interoperability", timeout=30).read().decode("utf-8", "ignore")
    import re
    bundle = re.search(r"main\.[0-9a-f]+\.js", page).group(0)
    js = urllib.request.urlopen(f"https://interopmatrix.broadcom.com/{bundle}", timeout=60).read().decode("utf-8", "ignore")
    key = re.search(r'"X-Auth-Key":"([^"]+)"', js).group(1)
    svc = re.search(r'simServiceUrl:"(https://interop\.esp\.[^"]+)"', js).group(1)
    open(kf, "w").write(key); open(sf, "w").write(svc)
    return key, svc


def matrix(svc, key, col_product, row_product):
    body = json.dumps({
        "columns": [{"product": col_product, "releases": []}],
        "rows": [{"product": row_product, "releases": []}],
        "isCollection": False, "isHidePatch": False, "isHideGenSupported": False,
        "isHideTechSupported": False, "isHideCompatible": False, "isHideIncompatible": False,
        "isHideNTCompatible": False, "isHideNotSupported": False,
    }).encode()
    req = urllib.request.Request(svc + "/products/interoperabilityMatrix", data=body,
                                 headers={"X-Auth-Key": key, "Accept": "application/json",
                                          "Content-Type": "application/json"}, method="POST")
    d = json.load(urllib.request.urlopen(req, timeout=120))
    cells = {}  # (col_version, row_version) -> status
    for cols in d.values():
        for c in cols:
            cv = c.get("version")
            for rows in (c.get("rowProdReleaseMap") or {}).values():
                for r in rows:
                    cells[(cv, r.get("version"))] = r.get("status")
    return cells


def product_of(node_id):
    return node_id.split(":", 1)[0]


def harvest(static_dir):
    """Every distinct drawn edge across every bundle: (fromProd, fromRel, toProd, toRel, untested, unverified)."""
    edges = {}
    for path in glob.glob(os.path.join(static_dir, "data*.json")):
        d = json.load(open(path))
        views = []
        if d.get("stackmap"):
            views.append(json.loads(d["stackmap"]) if isinstance(d["stackmap"], str) else d["stackmap"])
        for prod in (d.get("stackmaps") or {}).values():
            for body in prod.values():
                views.append(json.loads(body) if isinstance(body, str) else body)
        for v in views:
            for e in (v.get("edges") or []):
                rels = e.get("releases") or []
                if len(rels) != 2:
                    continue
                fp, tp = product_of(e["from"]), product_of(e["to"])
                key = (fp, rels[0], tp, rels[1], bool(e.get("untested")), bool(e.get("unverified")))
                edges[key] = edges.get(key, 0) + 1
    return edges


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("-")]
    verbose = "-v" in sys.argv or "--verbose" in sys.argv
    if not args:
        sys.exit("usage: validate-ui.py [-v] <static-site-dir>")
    static_dir = args[0]
    key, svc = discover()
    edges = harvest(static_dir)
    print(f"harvested {len(edges)} distinct drawn edges from {static_dir}\n")

    # group by product pair so each pair's matrix is fetched once
    pairs = {}
    for (fp, fr, tp, tr, untested, unverified) in edges:
        pairs.setdefault((fp, tp), []).append((fr, tr, untested, unverified))

    cell_cache = {}
    def cells_for(fp, tp):
        k = (fp, tp)
        if k not in cell_cache:
            cell_cache[k] = matrix(svc, key, PRODUCT_ID[fp], PRODUCT_ID[tp])
        return cell_cache[k]

    problems, checked = [], 0
    tot = {"compatible": 0, "not-tested": 0, "unverified": 0}
    rows = []          # per-pair report rows
    verbose_lines = []
    for (fp, tp), items in sorted(pairs.items()):
        cells = cells_for(fp, tp)
        comp = nt = unv = 0
        from_rels, to_rels = set(), set()
        for (fr, tr, untested, unverified) in items:
            checked += 1
            from_rels.add(fr); to_rels.add(tr)
            status = cells.get((fr, tr))
            label = f"{fp} {fr}  x  {tp} {tr}"
            if unverified:
                unv += 1; tot["unverified"] += 1
                if status is not None:
                    problems.append(f"[unverified-but-published] {label}: matrix status={STATUS.get(status,status)}")
                verbose_lines.append(f"  {label:52} UI=unverified  matrix={STATUS.get(status,status)}")
                continue
            if status is None:
                problems.append(f"[edge-on-missing-cell] {label}: UI drew a connector, matrix has NO cell")
            elif status not in (1, 3):
                problems.append(f"[edge-on-bad-cell] {label}: UI drew a connector, matrix says {STATUS.get(status,status)}")
            elif untested and status != 3:
                problems.append(f"[untested-mismatch] {label}: UI=untested, matrix={STATUS.get(status,status)}")
            elif not untested and status != 1:
                problems.append(f"[should-be-untested] {label}: UI=plain, matrix={STATUS.get(status,status)}")
            if status == 3:
                nt += 1; tot["not-tested"] += 1
            elif status == 1:
                comp += 1; tot["compatible"] += 1
            verbose_lines.append(f"  {label:52} UI={'untested' if untested else 'edge':8} matrix={STATUS.get(status,status)}")

        # coverage: of the compatible cells among the releases actually in play for this
        # pair, how many does the UI surface as connectors? The rest are legitimately
        # chain-blocked — compatible pairwise but with no complete stack behind them.
        in_scope = sum(1 for (cf, ct), st in cells.items()
                       if cf in from_rels and ct in to_rels and st in (1, 3))
        rows.append((f"{fp}x{tp}", len(items), comp, nt, unv,
                     len(from_rels), len(to_rels), in_scope))

    # ---- report ----
    print(f"checked {checked} edges across {len(pairs)} product pairs against the live matrix\n")
    print(f"{'pair':22}{'edges':>6}{'compat':>8}{'nt':>5}{'infer':>7}"
          f"{'fromR':>7}{'toR':>6}{'inScope✓':>10}")
    print("  " + "-" * 76)
    for name, n, comp, nt, unv, nf, ntr, insc in rows:
        print(f"{name:22}{n:>6}{comp:>8}{nt:>5}{unv:>7}{nf:>7}{ntr:>6}{insc:>10}")
    print("  " + "-" * 76)
    print(f"{'TOTAL':22}{checked:>6}{tot['compatible']:>8}{tot['not-tested']:>5}{tot['unverified']:>7}")
    print("\n  compat = matrix status 1 (compatible)   nt = status 3 (compatible, not tested)")
    print("  infer  = UI drew on trust (no cell)     inScope✓ = compatible cells among in-play")
    print("           releases; edges<=inScope✓ is expected (some pairs are chain-blocked).")

    # the not-tested edges are upstream's own hedge — worth eyeballing
    if tot["not-tested"]:
        print(f"\n  {tot['not-tested']} connector(s) rest on 'compatible, not tested' cells (upstream hedged).")

    if verbose:
        print("\n=== every drawn edge : UI claim vs live matrix ===")
        for ln in verbose_lines:
            print(ln)

    if problems:
        print(f"\n=== {len(problems)} MISMATCH(ES) ===")
        for p in problems[:200]:
            print("  " + p)
        sys.exit(1)
    print("\nPASS: every drawn connector matches the live Broadcom matrix.")


if __name__ == "__main__":
    main()
