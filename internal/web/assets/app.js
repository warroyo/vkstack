"use strict";

// The whole dataset is small and served from the same origin, so the app re-asks the
// server on every selection rather than mirroring the compatibility graph in the browser.
// That keeps one solver — the Go one — as the single source of truth for what "lit" means.

const state = {
  layers: [],
  lit: null,          // Set of node ids on the current path, or null when nothing is pinned
  pin: null,          // { product, version }
  recommended: null,
  edges: [],          // connections between adjacent layers, for the current selection
  baseCounts: {},     // layer key -> total node count, for the narrowing readout
};

const SVG_NS = "http://www.w3.org/2000/svg";
const svgEl = (tag, attrs = {}, text = null) => {
  const node = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v !== null && v !== undefined) node.setAttribute(k, v);
  }
  if (text !== null) node.textContent = text;
  return node;
};

const $ = (sel) => document.querySelector(sel);

const el = (tag, attrs = {}, ...kids) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined) continue;
    node.append(kid.nodeType ? kid : document.createTextNode(kid));
  }
  return node;
};

async function api(path) {
  const res = await fetch(path);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `request failed (${res.status})`);
  return body;
}

// --- the stack map ---------------------------------------------------------

async function loadStack() {
  let url = "/api/stackmap";
  if (state.pin) {
    url += `?product=${encodeURIComponent(state.pin.product)}` +
           `&version=${encodeURIComponent(state.pin.version)}`;
  }

  let data;
  try {
    data = await api(url);
  } catch (err) {
    $("#stack-error").textContent = err.message;
    $("#stack-error").classList.remove("hidden");
    return;
  }
  $("#stack-error").classList.add("hidden");

  state.layers = data.layers;
  state.lit = data.lit ? new Set(data.lit) : null;
  state.recommended = data.recommended || null;
  state.edges = data.edges || [];
  for (const layer of data.layers) state.baseCounts[layer.key] ??= layer.nodes.length;

  renderMap();
  renderStrata();
  renderPinControls();
  renderRecommended();
}

// --- the map ---------------------------------------------------------------
//
// A layered diagram drawn bottom-up: the selected version at the base, with real
// connectors branching up through Supervisor, VKS and VKr. Only the lit subgraph is
// drawn — the full cross-product is thousands of edges and reads as noise.

const MAP = {
  nodeH: 34,
  padX: 14,       // horizontal padding inside a node box
  gapX: 12,       // gap between sibling nodes
  rowGap: 74,     // vertical distance between layers
  marginX: 110,   // room for the layer label on the left
  marginY: 20,
  charW: 7.4,     // approximate width of one monospace character at 12.5px
};

function renderMap() {
  const host = $("#map");
  const caption = $("#map-caption");
  host.textContent = "";

  if (!state.pin) {
    host.append(el("p", { class: "map-empty" },
      "Pick a version below to see what branches out of it."));
    caption.textContent = "";
    return;
  }

  // Bottom-up: layers[0] is vCenter, and row 0 sits at the bottom of the drawing.
  const rows = state.layers.map((layer) => ({
    layer,
    nodes: layer.nodes.filter((n) => state.lit?.has(n.id)),
  }));

  const widths = new Map();
  for (const row of rows) {
    for (const n of row.nodes) {
      const chars = Math.max(n.label.length, (n.train || "").length);
      widths.set(n.id, Math.max(52, chars * MAP.charW + MAP.padX * 2));
    }
  }

  const rowWidth = (row) =>
    row.nodes.reduce((sum, n) => sum + widths.get(n.id), 0) +
    Math.max(0, row.nodes.length - 1) * MAP.gapX;

  const contentW = Math.max(...rows.map(rowWidth), 260);
  const width = contentW + MAP.marginX + 30;
  const height = (rows.length - 1) * MAP.rowGap + MAP.nodeH + MAP.marginY * 2;

  // Place every node, centring each row over the widest one.
  const pos = new Map();
  rows.forEach((row, i) => {
    const y = height - MAP.marginY - MAP.nodeH - i * MAP.rowGap;
    let x = MAP.marginX + (contentW - rowWidth(row)) / 2;
    for (const n of row.nodes) {
      const w = widths.get(n.id);
      pos.set(n.id, { x, y, w, cx: x + w / 2 });
      x += w + MAP.gapX;
    }
  });

  const svg = svgEl("svg", {
    viewBox: `0 0 ${width} ${height}`,
    width, height, class: "map-svg", role: "presentation",
  });

  // Connectors first, so nodes always sit on top of them.
  const links = svgEl("g", { class: "map-links" });
  for (const edge of state.edges) {
    const a = pos.get(edge.from);
    const b = pos.get(edge.to);
    if (!a || !b) continue;
    const y1 = a.y;                    // top of the lower node
    const y2 = b.y + MAP.nodeH;        // bottom of the upper node
    const mid = (y1 + y2) / 2;
    links.append(svgEl("path", {
      d: `M ${a.cx} ${y1} C ${a.cx} ${mid}, ${b.cx} ${mid}, ${b.cx} ${y2}`,
      class: "map-link",
    }));
  }
  svg.append(links);

  rows.forEach((row, i) => {
    const y = height - MAP.marginY - MAP.nodeH - i * MAP.rowGap;
    const label = svgEl("text", {
      x: MAP.marginX - 18, y: y + MAP.nodeH / 2 + 4,
      class: "map-layer-label", "text-anchor": "end",
    }, row.layer.label);
    svg.append(label);

    for (const n of row.nodes) {
      const p = pos.get(n.id);
      const g = svgEl("g", { class: nodeMapClass(row.layer, n), tabindex: "0", role: "button" });
      g.append(svgEl("rect", {
        x: p.x, y: p.y, width: p.w, height: MAP.nodeH, rx: 6, class: "map-node-box",
      }));
      if (n.train) {
        // Two lines: the Kubernetes minor, then the train it ships on. The trains are
        // not interchangeable, so they must never read as one node.
        g.append(svgEl("text", {
          x: p.cx, y: p.y + 13, "text-anchor": "middle", class: "map-node-text",
        }, n.label));
        g.append(svgEl("text", {
          x: p.cx, y: p.y + 24, "text-anchor": "middle", class: "map-node-train",
        }, n.train));
      } else {
        g.append(svgEl("text", {
          x: p.cx, y: p.y + MAP.nodeH / 2 + 4, "text-anchor": "middle", class: "map-node-text",
        }, n.label));
      }
      g.addEventListener("click", () => togglePin(row.layer.key, n));
      g.addEventListener("keydown", (ev) => {
        if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); togglePin(row.layer.key, n); }
      });
      g.addEventListener("mouseenter", (ev) => showPeek(ev.currentTarget, row.layer, n));
      g.addEventListener("mouseleave", hidePeek);
      svg.append(g);
    }
  });

  host.append(svg);

  const counts = rows.map((r) => `${r.nodes.length} ${r.layer.label}`).reverse().join(" · ");
  caption.textContent = `Branching from ${labelFor(state.pin.product)} ${state.pin.version} — ${counts}`;
}

function nodeMapClass(layer, node) {
  const classes = ["map-node"];
  if (state.pin && state.pin.product === layer.key && state.pin.version === pinVersionFor(node)) {
    classes.push("is-pinned");
  } else if (isRecommended(layer.key, node)) {
    classes.push("is-picked");
  }
  return classes.join(" ");
}

function renderStrata() {
  const root = $("#strata");
  root.textContent = "";

  for (const layer of state.layers) {
    const litCount = state.lit
      ? layer.nodes.filter((n) => state.lit.has(n.id)).length
      : layer.nodes.length;
    const total = state.baseCounts[layer.key] ?? layer.nodes.length;

    const count = state.lit && litCount !== total
      ? el("div", { class: "layer-count" },
          el("span", { class: "narrowed" }, String(litCount)), ` of ${total}`)
      : el("div", { class: "layer-count" }, `${total}`);

    const nodes = el("div", { class: "layer-nodes" });
    for (const node of layer.nodes) {
      nodes.append(nodeButton(layer, node));
    }

    root.append(
      el("div", { class: "layer" },
        el("div", { class: "layer-spine" },
          el("div", { class: "layer-name" }, layer.label), count),
        nodes));
  }
}

function nodeButton(layer, node) {
  const pinned = state.pin &&
    state.pin.product === layer.key &&
    state.pin.version === pinVersionFor(node);
  const lit = state.lit ? state.lit.has(node.id) : false;

  const classes = ["node"];
  if (node.noData) classes.push("nodata");
  if (pinned) classes.push("pinned");
  else if (state.lit && lit) classes.push("lit");
  else if (state.lit) classes.push("dim");
  if (!pinned && isRecommended(layer.key, node)) classes.push("picked");

  const label = [node.label];
  if (node.noData) label.push(" · no data yet");

  const button = el("button", {
      class: classes.join(" "),
      type: "button",
      "aria-pressed": pinned ? "true" : "false",
      onclick: () => togglePin(layer.key, node),
      onmouseenter: (ev) => showPeek(ev.currentTarget, layer, node),
      onfocus: (ev) => showPeek(ev.currentTarget, layer, node),
      onmouseleave: hidePeek,
      onblur: hidePeek,
    },
    label.join(""),
    node.train ? el("span", { class: "train" }, node.train) : null,
    node.detail
      ? el("span", { class: "detail" },
          layer.key === "vcenter" ? `on ESX ${node.detail}` : node.detail)
      : null);

  return button;
}

// A node stands for one or more releases. Pinning uses the newest, which is what the
// server lists first.
function pinVersionFor(node) {
  return node.releases && node.releases.length ? node.releases[0] : node.label;
}

function isRecommended(layerKey, node) {
  if (!state.recommended) return false;
  const chosen = state.recommended[layerKey];
  return !!chosen && node.releases?.includes(chosen);
}

function togglePin(layerKey, node) {
  const version = pinVersionFor(node);
  const same = state.pin && state.pin.product === layerKey && state.pin.version === version;
  state.pin = same ? null : { product: layerKey, version };
  hidePeek();
  loadStack();
}

function renderPinControls() {
  const label = $("#pin-label");
  const clear = $("#clear-pin");
  if (state.pin) {
    const layer = state.layers.find((l) => l.key === state.pin.product);
    label.textContent = `${layer ? layer.label : state.pin.product} ${state.pin.version}`;
    clear.classList.remove("hidden");
  } else {
    label.textContent = "";
    clear.classList.add("hidden");
  }
}

function renderRecommended() {
  const box = $("#recommended");
  box.textContent = "";
  if (!state.recommended) {
    box.classList.add("hidden");
    return;
  }
  box.classList.remove("hidden");

  const dl = el("dl");
  // Bottom-up, matching the map above it.
  for (const key of ["vcenter", "esx", "supervisor", "vks", "vkr"]) {
    const version = state.recommended[key];
    if (!version) continue;
    dl.append(el("dt", {}, labelFor(key)), el("dd", {}, version));
  }
  box.append(el("h2", {}, "Newest working stack from here"), dl);
}

function labelFor(key) {
  const known = { vcenter: "vCenter", esx: "ESX", supervisor: "Supervisor", vks: "VKS", vkr: "VKr" };
  return known[key] || key;
}

// --- peek popover ----------------------------------------------------------

function showPeek(anchor, layer, node) {
  const peek = $("#peek");
  peek.textContent = "";

  const lines = [];
  if (node.noData) {
    lines.push("Upstream publishes no compatibility data for this release yet.");
  } else if (node.releases?.length > 1) {
    lines.push(...node.releases);
  } else if (node.releases?.length === 1 && node.releases[0] !== node.label) {
    lines.push(node.releases[0]);
  }
  if (node.train) {
    lines.unshift(node.train === "vsc9"
      ? "Train vsc9 — ships with vCenter 9.x"
      : `Train ${node.train} — versioned independently of vCenter`);
  }
  if (layer.key === "vcenter" && node.hosts?.length) {
    lines.unshift(`Runs on ESX: ${node.hosts.join(", ")}`);
  }
  if (!lines.length) return;

  peek.append(el("div", { class: "peek-title" }, `${layer.label} ${node.label}`));
  for (const line of lines.slice(0, 14)) peek.append(el("div", {}, line));
  if (lines.length > 14) peek.append(el("div", {}, `+${lines.length - 14} more`));

  peek.classList.remove("hidden");
  const box = anchor.getBoundingClientRect();
  const size = peek.getBoundingClientRect();
  // Prefer above the node; flip below when there is no room.
  const top = box.top - size.height - 8;
  peek.style.top = `${top < 8 ? box.bottom + 8 : top}px`;
  peek.style.left = `${Math.max(8, Math.min(box.left, window.innerWidth - size.width - 8))}px`;
}

function hidePeek() {
  $("#peek").classList.add("hidden");
}

// --- the conceptual model --------------------------------------------------

const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
mermaid.initialize({
  startOnLoad: false,
  securityLevel: "strict",
  theme: prefersDark ? "dark" : "default",
  flowchart: { curve: "basis", nodeSpacing: 45, rankSpacing: 55, htmlLabels: true },
});

let modelLoaded = false;
async function loadModel() {
  if (modelLoaded) return;
  const data = await api("/api/model");
  try {
    const { svg } = await mermaid.render("model", data.mermaid);
    $("#model-diagram").innerHTML = svg;
  } catch (err) {
    $("#model-diagram").append(el("div", { class: "error" },
      `could not render diagram: ${err.message}`));
  }

  const list = $("#model-prose");
  list.textContent = "";
  for (const e of data.edges) {
    list.append(el("li", {},
      el("strong", {}, `${e.from} → ${e.to}`),
      el("span", { class: `evidence ${e.published ? "known" : "unknown"}` }, e.evidence),
      ` ${e.prose}`));
  }

  // Trains get their own callout: it is the fact people most often read past.
  const trains = $("#model-trains");
  trains.textContent = "";
  for (const t of data.trains || []) {
    trains.append(el("div", { class: "callout" },
      el("h3", {}, `${t.product} ships ${t.trains.length} trains`),
      el("p", {},
        ...t.trains.flatMap((name, i) => [
          i ? " and " : "",
          el("code", {}, name),
        ]),
        `. ${t.note}`)));
  }

  const grouping = $("#model-grouping");
  grouping.textContent = "";
  for (const note of data.grouping || []) grouping.append(el("li", {}, note));

  const unknowns = $("#model-unknowns");
  unknowns.textContent = "";
  for (const u of data.unknowns || []) {
    unknowns.append(el("div", { class: "unknown" },
      el("h3", {}, u.title),
      el("p", {}, u.detail),
      el("p", { class: "consequence" },
        el("strong", {}, "What it means: "), u.consequence),
      u.workaround
        ? el("p", { class: "workaround" }, el("strong", {}, "What to do: "), u.workaround)
        : null));
  }

  modelLoaded = true;
}

// --- tabs and boot ---------------------------------------------------------

function showView(name) {
  for (const v of ["stack", "model"]) {
    $(`#view-${v}`).classList.toggle("hidden", v !== name);
  }
  for (const btn of document.querySelectorAll("#tabs button")) {
    btn.classList.toggle("active", btn.dataset.view === name);
  }
  localStorage.setItem("interop.view", name);
  if (name === "model") loadModel();
}

async function boot() {
  try {
    const meta = await api("/api/meta");
    const mode = meta.refreshInterval
      ? `refreshes every ${meta.refreshInterval}`
      : (meta.readOnly ? "read-only" : "refresh on demand");
    $("#meta").textContent =
      `${meta.fetchedAt} · ${meta.ageHours}h old · ${mode} · ` +
      meta.products.map((p) => `${p.label} ${p.count}`).join("  ");
  } catch (err) {
    $("#meta").textContent = err.message;
  }

  await loadStack();
  // Start on the newest base version that has data, so the map is populated on arrival
  // rather than showing an empty frame.
  if (!state.pin) {
    const base = state.layers.find((l) => l.key === "vcenter");
    const newest = base?.nodes.find((n) => !n.noData);
    if (newest) {
      state.pin = { product: "vcenter", version: pinVersionFor(newest) };
      await loadStack();
    }
  }
  showView(localStorage.getItem("interop.view") || "stack");
}

for (const btn of document.querySelectorAll("#tabs button")) {
  btn.addEventListener("click", () => showView(btn.dataset.view));
}
$("#clear-pin").addEventListener("click", () => { state.pin = null; loadStack(); });
document.addEventListener("keydown", (ev) => {
  if (ev.key === "Escape" && state.pin) { state.pin = null; loadStack(); }
});
window.addEventListener("scroll", hidePeek, { passive: true });

boot();
