"""Check each hop of a `vkstack path` result against the VCF Upgrade Planner's upgrade tables.

Usage: python3 -I planner_crosscheck.py path.json compat-data.json k8s-upgrade-matrix.json

path.json is the result of `vkstack path` (CLI envelope) or `vkstack_path` (MCP payload).
The other two files come from
https://raw.githubusercontent.com/vmware/vcf-upgrade-planner/main/docs/.

Prints one line per step with the planner's verdict and vkstack's own `upgrade` field:
  supported    the planner lists the hop as supported
  blocked      the planner says no, with Broadcom's footnote (printed below the line)
  unpublished  no upstream answer: missing from the table, or a bare "no", which is how
               the planner's script stores upstream's null cells
VKr hops are read from the planner's own `vkr_upgrade_graph`, so for them a missing hop
is "not in graph" rather than unpublished.

Lines marked <-- need attention: every blocked hop, and every step where the planner and
vkstack disagree on whether upstream publishes the hop.
"""
import json, re, sys

path_file, compat_file, k8s_file = sys.argv[1:4]
# The CLI wraps the payload in an envelope; the MCP tool returns the payload alone.
result = json.load(open(path_file))
steps = result.get("data", result)["steps"]
compat = json.load(open(compat_file))["compatibility"]
k8s = json.load(open(k8s_file))

TABLE = {"vcenter": "vcenter_upgrade", "esx": "esx_upgrade", "nsx": "nsx_upgrade",
         "supervisor": "vsphere_supervisor_upgrade", "vks": "vks_upgrade", "avi": "avi_upgrade"}


def bare(v):
    return re.match(r"\d+\.\d+\.\d+", v).group(0)


def planner_verdict(p, src, dst):
    if p == "vkr":
        nxt = k8s["vkr_upgrade_graph"].get(bare(src))
        return ("supported" if nxt and bare(dst) in nxt else "not in graph"), ""
    if p not in TABLE:
        return "no table", ""
    cell = compat.get(TABLE[p], {}).get(src, {}).get(dst)
    if isinstance(cell, dict):
        status, note = cell.get("s"), cell.get("n", "")
    else:
        status, note = cell, ""
    if status == "yes":
        return "supported", note
    if status == "no" and note:
        return "blocked", note
    return "unpublished", ""


for i, s in enumerate(steps, 1):
    p, src, dst = s["product"], s["from"], s["to"]
    verdict, note = planner_verdict(p, src, dst)
    ours = s.get("upgrade", "?")
    attention = verdict == "blocked" or (
        p != "vkr" and verdict in ("supported", "unpublished") and ours in ("supported", "conditional", "unpublished")
        and (verdict == "unpublished") != (ours == "unpublished"))
    flag = "  <--" if attention else ""
    print(f"{i:2} {p:10} {src} -> {dst}: planner {verdict}, vkstack {ours}{flag}")
    if note:
        print(f"     note: {note}")
