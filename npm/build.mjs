// Stage the npm packages from a goreleaser build.
//
//   node npm/build.mjs 0.3.0            # after `goreleaser release` or `goreleaser build`
//
// The binaries published to npm are the same artifacts that go into the GitHub release:
// this reads goreleaser's own manifest rather than compiling anything, so there is no
// second build that could differ from the one people download and checksum.
//
// Output is npm/staging/, one directory per package, ready for `npm publish`. Directory
// names drop the scope, because a `/` cannot be one:
//
//   vkstack-mcp/                  @warroyo90/vkstack-mcp, the launcher, plus
//                                 optionalDependencies on all six
//   vkstack-mcp-linux-x64/        @warroyo90/vkstack-mcp-linux-x64, os/cpu gated
//   ...
//
// Publish the platform packages first. The launcher's optionalDependencies pin exact
// versions, so a root package that reaches the registry before them is briefly broken for
// anyone installing in that window.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "..");

const version = (process.argv[2] || "").replace(/^v/, "");
if (!/^\d+\.\d+\.\d+/.test(version)) {
  console.error("usage: node npm/build.mjs <version>   (e.g. 0.3.0, or 0.0.0-test)");
  process.exit(2);
}

const distDir = process.env.VKSTACK_DIST || path.join(repo, "dist");
const manifest = path.join(distDir, "artifacts.json");
if (!fs.existsSync(manifest)) {
  console.error(`build: ${manifest} not found — run goreleaser first`);
  process.exit(1);
}

// Go and npm disagree on names for the same machine. Everything downstream of this map
// is in npm's vocabulary.
const OS = { linux: "linux", darwin: "darwin", windows: "win32" };
const ARCH = { amd64: "x64", arm64: "arm64" };

const SCOPE = "@warroyo90";
const REPO_URL = "https://github.com/warroyo/vkstack";
const COMMON = {
  version,
  license: "MIT",
  homepage: REPO_URL,
  repository: { type: "git", url: `git+${REPO_URL}.git` },
  bugs: { url: `${REPO_URL}/issues` },
};

const artifacts = JSON.parse(fs.readFileSync(manifest, "utf8"))
  .filter((a) => a.type === "Binary");
if (artifacts.length === 0) {
  console.error("build: no Binary artifacts in artifacts.json");
  process.exit(1);
}

const staging = path.join(here, "staging");
fs.rmSync(staging, { recursive: true, force: true });

function write(dir, name, contents) {
  fs.mkdirSync(path.dirname(path.join(dir, name)), { recursive: true });
  fs.writeFileSync(path.join(dir, name), contents);
}

function writeJSON(dir, name, value) {
  write(dir, name, JSON.stringify(value, null, 2) + "\n");
}

// --- the platform packages -------------------------------------------------

const platforms = [];
const seen = new Set();

for (const a of artifacts) {
  const os = OS[a.goos];
  const cpu = ARCH[a.goarch];
  // goreleaser can emit several microarchitecture variants of the same os/arch pair
  // (amd64 v1, v2 …). npm has no way to express the distinction, so the first one wins
  // and the rest are skipped rather than silently overwriting each other.
  if (!os || !cpu || seen.has(`${os}-${cpu}`)) continue;
  seen.add(`${os}-${cpu}`);

  // The directory keeps the unscoped name; only the manifest carries the scope.
  const dirName = `vkstack-mcp-${os}-${cpu}`;
  const name = `${SCOPE}/${dirName}`;
  const exe = os === "win32" ? "vkstack.exe" : "vkstack";
  const dir = path.join(staging, dirName);

  fs.mkdirSync(path.join(dir, "bin"), { recursive: true });
  fs.copyFileSync(path.resolve(repo, a.path), path.join(dir, "bin", exe));
  // npm records the mode of files in the tarball, so setting it here is what makes the
  // binary executable on the far side of an install.
  fs.chmodSync(path.join(dir, "bin", exe), 0o755);

  writeJSON(dir, "package.json", {
    name,
    ...COMMON,
    description:
      `vkstack binary for ${os} ${cpu}. Installed automatically by ${SCOPE}/vkstack-mcp.`,
    os: [os],
    cpu: [cpu],
    files: ["bin"],
    // Nothing here is importable; the launcher resolves the manifest and spawns the file.
    preferUnplugged: true,
  });

  platforms.push(name);
}

if (platforms.length === 0) {
  console.error("build: artifacts.json had no os/arch pair npm can express");
  process.exit(1);
}

// --- the launcher ----------------------------------------------------------

const root = path.join(staging, "vkstack-mcp");
fs.mkdirSync(path.join(root, "bin"), { recursive: true });
fs.copyFileSync(path.join(here, "bin", "vkstack.js"), path.join(root, "bin", "vkstack.js"));
fs.chmodSync(path.join(root, "bin", "vkstack.js"), 0o755);
for (const doc of ["README.md", "AGENTS.md", "LICENSE"]) {
  fs.copyFileSync(path.join(repo, doc), path.join(root, doc));
}

writeJSON(root, "package.json", {
  name: `${SCOPE}/vkstack-mcp`,
  ...COMMON,
  description:
    "MCP server and CLI for VMware vCenter, ESX, vSphere Supervisor, VKS and VKr " +
    "compatibility, from the Broadcom interoperability matrix.",
  keywords: [
    "mcp", "modelcontextprotocol", "vmware", "vsphere", "vcenter", "esxi",
    "supervisor", "vks", "tanzu", "kubernetes", "compatibility", "interoperability",
  ],
  // Both names run the same launcher and forward every argument. Two bins pointing at
  // one file is not ambiguous to npx: it treats identical targets as aliases and picks
  // one, and failing that it matches a bin against the package name with the scope
  // stripped, which `vkstack-mcp` also satisfies. `vkstack` is there because a global
  // install should leave the tool's own name on PATH — this ships the whole CLI, and
  // `vkstack refresh` has to be typeable.
  bin: { "vkstack-mcp": "bin/vkstack.js", vkstack: "bin/vkstack.js" },
  engines: { node: ">=18" },
  files: ["bin", "README.md", "AGENTS.md", "LICENSE"],
  optionalDependencies: Object.fromEntries(platforms.map((p) => [p, version])),
});

// --- report ----------------------------------------------------------------

console.log(`staged ${platforms.length + 1} packages at ${version} in ${staging}`);
for (const name of [...platforms, `${SCOPE}/vkstack-mcp`]) console.log(`  ${name}`);

// A smoke test of the thing being published, not of the source tree: run the staged
// launcher against the staged package for whatever machine this is. It catches a broken
// resolve, a missing binary and a lost permission bit before anything reaches npm.
const selfDir = `vkstack-mcp-${process.platform}-${process.arch}`;
if (platforms.includes(`${SCOPE}/${selfDir}`)) {
  const modules = path.join(root, "node_modules");
  // The link has to sit under the scope directory, because that is where the launcher's
  // require.resolve of a scoped name will look.
  fs.mkdirSync(path.join(modules, SCOPE), { recursive: true });
  fs.symlinkSync(
    path.join(staging, selfDir), path.join(modules, SCOPE, selfDir), "junction");
  try {
    const out = execFileSync(
      process.execPath, [path.join(root, "bin", "vkstack.js"), "--version"],
      { encoding: "utf8" });
    console.log(`\nlauncher check: ${out.trim()}`);
  } finally {
    // node_modules must not end up in the tarball: it would pin a copy of the platform
    // package inside the launcher and defeat the os/cpu gating.
    fs.rmSync(modules, { recursive: true, force: true });
  }
} else {
  console.log(`\nlauncher check skipped: no staged package for ${selfDir}`);
}
