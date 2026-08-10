#!/usr/bin/env node
"use strict";

// The npm face of vkstack: a launcher that finds the prebuilt binary for this platform
// and hands it the argument list untouched.
//
// The binary itself ships in one of six per-platform packages, declared as optional
// dependencies of this one. npm installs only the package whose `os` and `cpu` match, so
// a machine downloads one binary rather than six. That is the esbuild arrangement, and it
// is what makes `npx -y @warroyo90/vkstack-mcp mcp` viable as an MCP server command: no
// postinstall step, no download at run time, nothing to trust beyond the registry.
//
// This file is not a wrapper around a subset of the CLI. Every argument is forwarded, so
// `vkstack refresh` and `vkstack stack vcenter 8.0U3k` work here exactly as they do for a
// binary installed from a release archive.

const { spawnSync } = require("child_process");
const path = require("path");

// npm names platforms and architectures differently from Go. process.platform is already
// npm's spelling (`win32`, not `windows`); process.arch is `x64` where Go says `amd64`.
// The staging script maps the Go side, so the two meet here on npm's terms.
const SUPPORTED = new Set([
  "darwin-arm64", "darwin-x64",
  "linux-arm64", "linux-x64",
  "win32-arm64", "win32-x64",
]);

function binaryPath() {
  const target = `${process.platform}-${process.arch}`;
  if (!SUPPORTED.has(target)) {
    fail(`vkstack has no prebuilt binary for ${target}.`,
      `Releases cover ${[...SUPPORTED].join(", ")}.`,
      "With a Go toolchain: go install github.com/warroyo/vkstack/cmd/vkstack@latest");
  }

  const pkg = `@warroyo90/vkstack-mcp-${target}`;
  const exe = process.platform === "win32" ? "vkstack.exe" : "vkstack";

  // Resolve the package's manifest rather than the binary itself. A file with no
  // extension is not something Node's module resolver is meant to load, and going
  // through package.json keeps this working whatever the file is called.
  let root;
  try {
    root = path.dirname(require.resolve(`${pkg}/package.json`));
  } catch {
    fail(`the ${pkg} package is not installed.`,
      "It is an optional dependency of @warroyo90/vkstack-mcp, so this usually means the",
      "install ran with optional dependencies disabled (--no-optional, or an npm mirror",
      "that drops them).",
      "",
      `Fix it with: npm install ${pkg}`,
      "Or install the binary directly:",
      "  curl -fsSL https://raw.githubusercontent.com/warroyo/vkstack/main/install.sh | sh");
  }
  return path.join(root, "bin", exe);
}

function fail(...lines) {
  for (const line of lines) console.error(line ? `vkstack: ${line}` : "");
  process.exit(1);
}

const bin = binaryPath();

// stdio: "inherit" is load-bearing. MCP speaks JSON-RPC over stdin and stdout, so the
// binary has to own the real file descriptors — anything that pipes through this process
// would put a buffer in the middle of the protocol.
const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });

if (result.error) {
  if (result.error.code === "ENOENT") {
    fail(`${bin} is missing from the installed platform package.`,
      "Reinstalling should restore it: npm install --force @warroyo90/vkstack-mcp");
  }
  if (result.error.code === "EACCES") {
    fail(`${bin} is not executable.`,
      "Some archive tools and npm mirrors drop the permission bit.",
      `Fix it with: chmod +x ${bin}`);
  }
  fail(`could not run ${bin}: ${result.error.message}`);
}

// A child killed by a signal did not exit with a status, and reporting that as 1 would
// make a Ctrl-C indistinguishable from a real failure. Re-raise it on this process so the
// shell above sees what actually happened.
if (result.signal) {
  process.kill(process.pid, result.signal);
} else {
  process.exit(result.status === null ? 1 : result.status);
}
