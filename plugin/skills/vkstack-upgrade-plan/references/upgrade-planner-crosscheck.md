# Cross-check against the VCF Upgrade Planner

Broadcom publishes the VCF Upgrade Planner at https://vmware.github.io/vcf-upgrade-planner/ (source: https://github.com/vmware/vcf-upgrade-planner). The site is static and has two tools:

- **VCF Upgrade Planner** (`index.html`): the full stack, including vCenter, ESX, NSX, Avi, Supervisor, VKS, VCF Operations and Automation, HCX, SRM and others. It reads `docs/compat-data.json`. **This is the main comparison.**
- **VC / Supervisor / VKS Upgrade Planner** (`vks-upgrade-planner.html`): vCenter, Supervisor, VKS and VKr only, with a hop solver. It reads `docs/k8s-upgrade-matrix.json`. Use it for VKr, which the VCF data doesn't carry.

Use the planner as a second opinion, not as a source. vkstack and the Broadcom docs decide the plan. The planner's job is to show where the plan might be wrong, so those spots get checked before the plan goes out.

Notes below were true as of 2026-10-22. Re-read the files' keys before relying on them.

## What the planner data adds

**Upgrade paths.** `compat-data.json` mirrors the interop matrix's upgrade-path view (`/external/upgrades`), the same data `vkstack refresh` mirrors into its `upgrades` table. It has source → target tables for vCenter, ESX, NSX, Supervisor, VKS and Avi. The two should agree, so this is a check on vkstack's snapshot, not new information. One trap: upstream sends `null` for cells it has no answer for, and the planner's script turns `null` into `"no"`. Only a `no` that carries a Broadcom footnote is a real block. A bare `no` means unpublished, which vkstack reports as `upgrade: "unpublished"`.

**Freshness.** A weekly GitHub Action refreshes `compat-data.json` from Broadcom's API, and `_meta.last_updated` gives the date. If it's newer than the vkstack snapshot and its `versions` lists name releases vkstack doesn't have, vkstack is stale. Tell the operator to run `vkstack refresh`, and don't treat the gap as a disagreement. Compare `versions`, not the `compatibility` keys. The tables also carry rows for releases that aren't out yet, every pair `no`. As of 2026-10-22 those are Supervisor vsc0.1.16 and vsc9.1.2.0, and VKS 3.8.1. Ignore them.

## Where the planner is weaker than vkstack

- **Supervisor × Avi isn't checked.** The page looks up `compatibility.supervisor_avi`, but `compat-data.json` has no such key, so the check never reports anything. Its Avi verdict is otherwise a major-version rule: Avi 30+ is "supported" for VCF 9. That misses build-level gaps such as Avi 32.1.2 being the only 32.1 build that pairs with vsc0 Supervisors. vkstack is the authority for every Avi constraint except the upgrade hop itself.
- **NSX × Avi, NSX × Supervisor, vCenter × Supervisor and VKS × VKr** aren't in `compat-data.json`. vCenter × Supervisor and VKS × VKr are in `k8s-upgrade-matrix.json`.
- **Avi upgrade paths are sparse.** `avi_upgrade` has only a couple of rows (e.g. 32.1.1 and 32.1.2 → 32.1.3). Missing rows mean "not published", not "blocked". Use the Avi upgrade checklist in `doc-checks.md` for Avi hops.
- **Version lists are capped at 50 per product.** Older releases may be missing from `versions` but still appear as keys in the `compatibility` tables.
- **`k8s-upgrade-matrix.json` is refreshed by hand.** The weekly Action doesn't rebuild it, so it can lag `compat-data.json`. Its `golden_path_*` and `lifecycle` keys are editorial, not interop data.

## Fetch it

```sh
base=https://raw.githubusercontent.com/vmware/vcf-upgrade-planner/main/docs
curl -sSfL $base/compat-data.json -o compat-data.json             # ~4 MB
curl -sSfL $base/k8s-upgrade-matrix.json -o k8s-upgrade-matrix.json
jq -r '._meta.last_updated' compat-data.json
curl -s "https://api.github.com/repos/vmware/vcf-upgrade-planner/commits?path=docs/k8s-upgrade-matrix.json&per_page=1" | jq -r '.[0].commit.author.date'
```

Save both to a scratch directory, not into the user's repo.

## `compat-data.json` layout

- `products`: planner key → Broadcom product ID. The keys differ from vkstack's: `vsphere_supervisor` (vkstack `supervisor`), `vks`, `vcenter`, `esx`, `nsx`, `avi`.
- `versions.<product>`: up to 50 releases, newest first, as `{value, label, group, ga_date}`.
- `compatibility.<pair>`: nested `{row: {col: status}}`. Status is `"yes"`, `"no"`, or `{"s": "yes"|"no", "n": "<footnote>"}`.
  - **Compatibility pairs:** `vcenter_esx`, `vcenter_nsx`, `nsx_esx`, `vcenter_avi`, `supervisor_vks`.
  - **Upgrade tables:** `<product>_upgrade`, keyed source → target. Products: `vcenter`, `esx`, `nsx`, `vsphere_supervisor`, `vks`, `avi`.

```sh
# Is vCenter 8.0U3j → 9.1.0.0 a supported upgrade, and if not, why?
jq '.compatibility.vcenter_upgrade["8.0U3j"]["9.1.0.0"]' compat-data.json
# Which VKS releases can 3.3.3 upgrade to directly?
jq -r '.compatibility.vks_upgrade["3.3.3"] | to_entries[] | select((.value | if type=="object" then .s else . end)=="yes") | .key' compat-data.json
```

## `k8s-upgrade-matrix.json` keys

| Key | Use |
|---|---|
| `vks_vkr_compatibility` | VKS × VKr, which `compat-data.json` lacks |
| `vkr_upgrade_graph` | Guest-cluster bumps |
| `sv_vc_compatibility` | vCenter × Supervisor, which `compat-data.json` lacks |
| `vc_embedded_vks` | The VKS each vCenter installs, unless the user upgrades VKS separately |
| `vc_major_upgrade_requires_sv`, `vc_minor_upgrade_requires_post_sv` | Ordering rules around vCenter hops (`8.x->9.x`, `9.x->9.x`) |
| `lifecycle`, `vkr_status` | End-of-support dates and superseded VKr builds |

## Version strings

- **vCenter, ESX, NSX, Avi, Supervisor:** the same strings as vkstack.
- **VKS:** the same, including upstream's `3.6.3+1.35` (no `v`) in `compat-data.json`. `k8s-upgrade-matrix.json` normalizes it to `3.6.3+v1.35`. Compare on the part before `+`.
- **VKr:** `k8s-upgrade-matrix.json` uses bare versions (`1.28.8`). vkstack keeps upstream's labels (`1.28.8 (TKr 1.28.8 for vSphere 8.x)`). Compare on the leading `x.y.z`.

## How to run the comparison

Do this after the doc checks, once the path is built and every state passes `vkstack_check`:

1. **Freshness.** Compare `_meta.last_updated` with the vkstack `snapshot.fetchedAt`. If the planner's `versions` lists name releases vkstack lacks, say vkstack is stale before going further.
2. **Every hop.** Run `scripts/planner_crosscheck.py path.json compat-data.json k8s-upgrade-matrix.json` over the saved `vkstack path` result. It looks up each step's source → target in that product's `<product>_upgrade` table (VKr in `vkr_upgrade_graph`). `vkstack_path` already avoids blocked hops, so a `blocked` line means the two snapshots differ: run `vkstack refresh`, re-run the path, and check again. If it persists, re-run the path with that target excluded and report the disagreement.
   - `yes`: supported.
   - `no` with a footnote: blocked. Quote the footnote, and fix the plan or confirm in the KBs.
   - `no` without a footnote, or missing: not published. Check the product's release notes or upgrade checklist (`doc-checks.md`).
3. **Every state.** Check the pairs the planner has (vCenter × ESX, vCenter × NSX, NSX × ESX, vCenter × Avi, Supervisor × VKS, and VKS × VKr from the VKS matrix). vkstack has already checked these, so a difference is a data disagreement, not a plan error. Report it with both answers.
4. **Optional.** Enter the current and target stack in the VCF planner page and compare its phase order with the plan.

## Reporting

Put the result in the **internal** plan only, as a short "Cross-check: VCF Upgrade Planner" section:

- Both data dates (`compat-data.json`, `k8s-upgrade-matrix.json`) and the vkstack snapshot date.
- The hop table: step, product, source → target, planner status, footnote if any.
- "Agrees" for the pairs if nothing differs. Otherwise one row per difference: step, pair, vkstack's answer, planner's answer, and how it was resolved (release notes, KB, or open question).
- A note that the planner doesn't check Supervisor × Avi, so Avi constraints rest on vkstack alone.

Never change the plan just because the planner disagrees. The exception is a blocked hop with a Broadcom footnote. That is upstream's own upgrade-path data, so fix the route or confirm the block in the KBs, and say the vkstack snapshot was behind. Leave the cross-check out of the customer-facing version.
