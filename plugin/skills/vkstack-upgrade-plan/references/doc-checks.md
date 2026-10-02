# Broadcom doc checks for upgrade plans

The matrix answers "can these run together". These pages answer "is this hop a supported upgrade" and "in what order". Fetch the current page every time. These rules change between patch releases, and the notes below were true as of 2026-09-30.

## vCenter: back-in-time upgrade rules

A vCenter (or ESX, or NSX) upgrade is blocked when the target build was released before the source build. This matters most for vSphere 8 → VCF 9. The newest 8.0U3 patches are newer than early 9.x builds.

| Source | → 9.0.0.0 / 9.0.1.0 / 9.0.2.0 | → 9.1.0.x | → 9.1.1.0 |
|---|---|---|---|
| 8.0U3 – 8.0U3i | U3h/U3i blocked | supported | supported |
| 8.0U3j – 8.0U3k | blocked | blocked (import only) | supported |

Pages:
- KB 450972 — vSphere and VCF 9.1 "Back-in-Time" Release Support: https://knowledge.broadcom.com/external/article/450972/vsphere-and-vmware-cloud-foundation-vcf.html
- KB 448135 — Back-in-Time Upgrade Restriction for vSphere 8.0 Update 3j and later (resolved by VCF 9.1.1.0): https://knowledge.broadcom.com/external/article/448135
- vCenter 8.0 Update 3i release notes (8.0U3i → 9.0.x blocked): https://techdocs.broadcom.com/us/en/vmware-cis/vsphere/vsphere/8-0/release-notes/vcenter-server-update-and-patch-release-notes/vsphere-vcenter-server-80u3i-release-notes.html
- William Lam, VCF 9.1.1 enhancements (9.1.1 gives 8.0U3j–U3k, VCF 5.2.4 and 9.0.2 EP02 a supported path). This is a secondary source; confirm in the KBs: https://williamlam.com/2026/09/10-exciting-enhancements-in-vmware-cloud-foundation-9-1-1.html

How to use it: if the backbone path needs an intermediate vCenter (e.g. 9.1.0.0 because an optional product isn't listed with 9.1.1.0), the source patch level must reach that intermediate. Tell the user not to patch vCenter 8 past that level before the hop.

## vCenter: pre-upgrade blockers (9.1)

These don't change the route, but they stop the hop. Put each one in the plan's pre-flight checklist and on the vCenter step it blocks.

- KB 455737 — Fixing VUMDB inconsistency issues after running vLCM reset script on 8.0U3 to 9.0.x: https://knowledge.broadcom.com/external/article?articleNumber=455737. If `updatemgr-utility.py reset-db` was ever run on the vCenter, the VUM database is missing the `vm_placement_policy` column and three related tables. The VCF Diagnostic Tool then reports `[FAIL] VUM SCHEMA CHECK`, which must pass before upgrading to 9.1. Left unfixed, it can also affect the Supervisor and load balancers after the upgrade to 9.1.0/9.1.1. The fix is a checker SQL script, then a fixer SQL script, from the KB. As of 2026-10-01, a permanent fix is planned for the next 9.1 release, so re-check whether the target 9.1 build still needs it. Applies to every 8.0U3 or 9.0 → 9.1 vCenter hop.

## Upgrade order across components (9.1)

Upgrading to vSphere Foundation 9.1.x: https://techdocs.broadcom.com/us/en/vmware-cis/vcf/vcf-9-0-and-later/9-1/deployment/upgrading-your-vsphere-foundation-to-9-1.html

The documented order is VCF Operations → vCenter → VCF Operations for logs → VKS → Supervisor → ESX → VMware Tools → VM compatibility → vSAN. So on an 8 → 9 move, the Supervisor goes to the vsc9 train before ESX goes to 9.x. The ESX 8 + vsc9 state in between is transitional. For VCF (not VVF), use the matching "Upgrading to VMware Cloud Foundation 9.1.x" page.

## VKS

- VKS 3.6 release notes (direct upgrade needs VKS ≥ 3.3 installed, clusters on VKr ≥ 1.32, Supervisor ≥ 1.30 / vCenter ≥ 8.0U3g): https://techdocs.broadcom.com/us/en/vmware-cis/vcf/vcf-service-administration-and-development/9-1/release-notes/vks-release-notes/vmware-tanzu-kubernetes-grid-service-36-release-notes.html
- VKS 3.5 / 3.7 release notes sit beside it (same path, `-35-` / `-37-`).
- VKS 3.3.3 and earlier release notes (3.3 needs clusters on VKr ≥ 1.28; 3.2 needs TKr ≥ 1.27): https://techdocs.broadcom.com/us/en/vmware-cis/vsphere/vsphere-supervisor/8-0/release-notes/vmware-tanzu-kubernetes-grid-service-release-notes.html

Pattern: each VKS release names a minimum installed VKS for a direct upgrade, and a minimum VKr on every cluster. The VKr floor usually forces cluster bumps *before* the VKS hop.

## Avi Load Balancer

- Checklist for Upgrade to 32.1.x (minimum upgradable 22.1.4; 22.1.4–22.1.7, 30.1.x, 30.2.x, 31.1.x, 31.2.1–31.2.2 → any 32.1.x; 31.2.3 → 32.1.3 only; valid license required, upgrades blocked on Eval/Trial): https://techdocs.broadcom.com/us/en/vmware-security-load-balancing/avi-load-balancer/avi-load-balancer/32-1/vmware-avi-load-balancer-release-notes/checklist-for-upgrade-to-32-1-x.html
- 32.1.1 release notes, known issue AV-283255 (after 22.1 → 32.1.x, Controller Management access changes aren't applied to iptables): https://techdocs.broadcom.com/us/en/vmware-security-load-balancing/avi-load-balancer/avi-load-balancer/32-1/vmware-avi-load-balancer-release-notes/release-notes-for-avi-load-balancer-version-32-1-1.html
- Other lines have their own "Checklist for Upgrade to <line>.x" page under the same tree.

## Supervisor

- vSphere Supervisor 8.0 release notes: https://techdocs.broadcom.com/us/en/vmware-cis/vsphere/vsphere-supervisor/8-0/release-notes/vmware-vsphere-supervisor-80-release-notes.html. As of 2026-09-30 they don't describe the vsc0 → vsc9 switch itself; say so if asked.

## Interop matrix

- https://interopmatrix.broadcom.com: the upstream of vkstack. It also has an upgrade-path view that vkstack does not mirror.
