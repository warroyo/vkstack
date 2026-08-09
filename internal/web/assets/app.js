"use strict";

// The whole dataset is small and served from the same origin, so the app re-asks the
// server on every selection rather than mirroring the compatibility graph in the browser.
// That keeps one solver — the Go one — as the single source of truth for what "lit" means.

const state = {
  layers: [],
  lit: null,          // Set of node ids on the current path, or null when nothing is pinned
  pin: null,          // { product, version }
  recommended: null,
  provenance: null,   // how much of the recommended stack is a lookup vs taken on trust
  edges: [],          // connections between adjacent layers, for the current selection
  hideLegacy: true,   // mirrors the interop site's own "hide legacy releases" checkbox
  baseCounts: {},     // layer key -> total node count, for the narrowing readout
  meta: null,         // the cache snapshot this page is reading, for the manifest
  filter: "",         // free-text narrowing of the version lists
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

// A static build has no server. Every answer the map can give was produced at build time
// by the same handlers that would have answered over HTTP, and shipped as one bundle, so
// selecting a version is a lookup rather than a round trip. `vkstack static` sets the
// flag; `vkstack serve` leaves it unset and the requests below are real.
let bundle = null;
function siteData() {
  if (!bundle) {
    bundle = fetch("data.json").then((res) => {
      if (!res.ok) throw new Error(`could not load the site data (${res.status})`);
      return res.json();
    });
  }
  return bundle;
}

async function apiStatic(path) {
  const url = new URL(path, location.href);
  const data = await siteData();

  switch (url.pathname) {
    case "/api/meta":
      return data.meta;
    case "/api/stackmap": {
      const product = url.searchParams.get("product");
      const version = url.searchParams.get("version");
      if (!product || !version) return data.stackmap;
      const pinned = data.stackmaps?.[product]?.[version];
      // Only reachable if the bundle and the UI disagree about what is clickable, which
      // means the build is stale rather than the selection being wrong.
      if (!pinned) throw new Error(`this build has no answer for ${product} ${version}`);
      return pinned;
    }
  }
  throw new Error(`unknown path ${url.pathname}`);
}

async function api(path) {
  if (window.VKSTACK_STATIC) return apiStatic(path);
  const res = await fetch(path);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `request failed (${res.status})`);
  return body;
}

// --- the stack map ---------------------------------------------------------

// Clicks are not queued and the solve takes 10-260ms, so two quick clicks put two
// requests in flight. Each one takes a ticket; only the newest is allowed to land.
// Without this the slower answer to an older click can overwrite the newer one, and the
// map ends up showing a stack for a version nobody has selected.
let inFlight = 0;

async function loadStack() {
  let url = "/api/stackmap";
  if (state.pin) {
    url += `?product=${encodeURIComponent(state.pin.product)}` +
           `&version=${encodeURIComponent(state.pin.version)}`;
  }

  const ticket = ++inFlight;
  // The previous answer stays on screen, dimmed, until this one lands. A blank frame
  // during a slow round trip reads as a broken tool rather than as work in progress.
  $("#map").classList.add("is-solving");

  let data;
  try {
    data = await api(url);
  } catch (err) {
    if (ticket !== inFlight) return;
    $("#map").classList.remove("is-solving");
    showStackError(err.message);
    return;
  }
  if (ticket !== inFlight) return; // a newer click has already been asked
  $("#map").classList.remove("is-solving");
  $("#stack-error").classList.add("hidden");

  state.layers = data.layers;
  state.lit = data.lit ? new Set(data.lit) : null;
  state.recommended = data.recommended || null;
  state.provenance = data.provenance || null;
  state.edges = data.edges || [];

  renderMap();
  renderStrata();
  renderPinControls();
  renderRecommended();
}

// showStackError reports a failed solve and, when the pin caused it, offers the way out.
//
// A pin can now arrive from a link someone saved weeks ago, naming a release a later
// refresh dropped. That used to leave the page dead: error text, an empty map, and no
// Clear button, since renderPinControls never ran.
function showStackError(message) {
  const box = $("#stack-error");
  box.textContent = "";
  box.append(el("span", {}, message));
  if (state.pin) {
    box.append(el("button", {
      class: "ghost inline",
      type: "button",
      onclick: () => setPin(null),
    }, "Clear the selection"));
  }
  box.classList.remove("hidden");
  renderPinControls();
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

// The map is reconciled, not rebuilt.
//
// Wiping the SVG on every click destroyed the one thing a reader most wants to see: what
// their choice did. Which Supervisor options went dark, which appeared, whether the
// future got wider or narrower — all of that lives in the difference between two frames,
// and a full redraw throws it away. So nodes are kept by id across renders: survivors
// move, newcomers fade in, the departed fade out. Motion means something changed.
//
// A node's position lives on its group's transform rather than on the rect, so a move is
// one animatable property instead of a rewrite of every child coordinate.
const mapDOM = {
  svg: null,
  links: null,
  labels: null,
  nodes: new Map(),   // node id -> <g>
  edges: new Map(),   // "from|to" -> <path>
};

function resetMap() {
  mapDOM.svg = null;
  mapDOM.links = null;
  mapDOM.labels = null;
  mapDOM.nodes.clear();
  mapDOM.edges.clear();
  $("#map").textContent = "";
}

function renderMap() {
  const host = $("#map");
  const caption = $("#map-caption");
  // A redraw moves the map out from under the pointer, and the mouseleave that would
  // have ended the trace may never fire. Start every frame untraced.
  clearTrace();

  if (!state.pin) {
    resetMap();
    host.append(el("p", { class: "map-empty" },
      "Pick a version below to see what branches out of it."));
    caption.textContent = "";
    return;
  }

  // Bottom-up: layers[0] is vCenter, and row 0 sits at the bottom of the drawing.
  // The pinned node is always drawn: hiding your own selection because the legacy
  // filter came on afterwards leaves a map that answers a question nobody asked.
  const rows = state.layers.map((layer) => ({
    layer,
    nodes: layer.nodes.filter((n) =>
      state.lit?.has(n.id) &&
      (!(state.hideLegacy && n.legacy) || isPinned(layer, n))),
  }));

  // A pin with no complete stack is not the same as no pin. Both used to draw nothing.
  if (rows.every((r) => r.nodes.length === 0)) {
    resetMap();
    host.append(el("p", { class: "map-empty is-dead-end" },
      `No complete stack exists with ${labelFor(state.pin.product)} ${state.pin.version}` +
      (state.hideLegacy ? " among non-legacy releases." : ".")));
    caption.textContent = "";
    return;
  }

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

  const svg = ensureSVG(host, width, height);
  reconcileLabels(rows, height);
  reconcileEdges(pos);
  reconcileNodes(rows, pos);

  const counts = rows.map((r) => `${r.nodes.length} ${r.layer.label}`).reverse().join(" · ");
  caption.textContent = `Branching from ${labelFor(state.pin.product)} ${state.pin.version} — ${counts}`;
  return svg;
}

// ensureSVG creates the drawing once and only resizes it afterwards, so everything inside
// survives from one selection to the next.
function ensureSVG(host, width, height) {
  if (!mapDOM.svg) {
    host.textContent = "";
    mapDOM.svg = svgEl("svg", { class: "map-svg", role: "presentation" });
    mapDOM.links = svgEl("g", { class: "map-links" });     // under the nodes
    mapDOM.labels = svgEl("g", { class: "map-labels" });
    mapDOM.svg.append(mapDOM.links, mapDOM.labels);
    host.append(mapDOM.svg);
  }
  mapDOM.svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  mapDOM.svg.setAttribute("width", width);
  mapDOM.svg.setAttribute("height", height);
  return mapDOM.svg;
}

function reconcileLabels(rows, height) {
  const wanted = rows.map((row, i) => ({
    text: row.layer.label,
    y: height - MAP.marginY - MAP.nodeH - i * MAP.rowGap + MAP.nodeH / 2 + 4,
  }));
  const existing = [...mapDOM.labels.children];
  wanted.forEach((w, i) => {
    const node = existing[i] || mapDOM.labels.appendChild(svgEl("text", {
      class: "map-layer-label", "text-anchor": "end", x: MAP.marginX - 18,
    }));
    node.setAttribute("y", w.y);
    node.textContent = w.text;
  });
  existing.slice(wanted.length).forEach((n) => n.remove());
}

// reconcileEdges keys a connector by its endpoints, so a curve that survives a selection
// bends to its new shape instead of disappearing and coming back.
function reconcileEdges(pos) {
  const seen = new Set();
  for (const edge of state.edges) {
    const a = pos.get(edge.from);
    const b = pos.get(edge.to);
    if (!a || !b) continue;
    const key = `${edge.from}|${edge.to}`;
    seen.add(key);
    const y1 = a.y;                    // top of the lower node
    const y2 = b.y + MAP.nodeH;        // bottom of the upper node
    const mid = (y1 + y2) / 2;
    // A connection carries the matrix's own confidence: solid for a tested yes, dashed
    // for "compatible, not tested", dotted for a pair upstream never evaluated.
    const cls = ["map-link"];
    if (edge.unverified) cls.push("is-unverified");
    else if (edge.untested) cls.push("is-untested");
    if (edge.footnote) cls.push("has-footnote");

    // Two paths per connection: the hairline that is drawn, and a fat transparent one
    // that is aimed at. A 1.4px stroke is a 1.4px hover target, which is not a target.
    let group = mapDOM.edges.get(key);
    if (!group) {
      group = svgEl("g", {
        class: "map-edge is-entering", "data-from": edge.from, "data-to": edge.to,
      });
      group.append(svgEl("path", { class: "map-hit" }), svgEl("path", {}));
      mapDOM.links.append(group);
      mapDOM.edges.set(key, group);
      bindMapEdge(group, edge.from, edge.to);
      requestAnimationFrame(() => group.classList.remove("is-entering"));
    }
    const [hit, line] = group.children;
    line.setAttribute("class", cls.join(" "));
    const d = `M ${a.cx} ${y1} C ${a.cx} ${mid}, ${b.cx} ${mid}, ${b.cx} ${y2}`;
    hit.setAttribute("d", d);
    line.setAttribute("d", d);
    group.querySelector("title")?.remove();
    const why = edgeProvenance(edge);
    if (why) group.append(svgEl("title", {}, why));
  }
  for (const [key, group] of mapDOM.edges) {
    if (seen.has(key)) continue;
    mapDOM.edges.delete(key);
    fadeOut(group);
  }
}

// --- tracing a connection ----------------------------------------------------
//
// A lit map can carry dozens of connectors, and where two layers are both wide the curves
// cross often enough that following one by eye is guesswork. Hovering resolves it: the
// connection under the pointer, and the two releases it is actually about, stay at full
// strength while everything else recedes. Hovering a node does the same for every
// connection that touches it, which is the question people actually arrive with — what
// does this version reach.

function bindMapEdge(group, from, to) {
  group.onmouseenter = () => traceEdges([`${from}|${to}`], [from, to]);
  group.onmouseleave = () => clearTrace();
}

// traceEdges lights the named connections and their endpoints, and dims the rest.
function traceEdges(keys, nodeIDs) {
  if (!mapDOM.svg || !keys.length) return;
  const hot = new Set(keys);
  for (const [key, group] of mapDOM.edges) {
    group.classList.toggle("is-hot", hot.has(key));
  }
  const lit = new Set(nodeIDs);
  for (const [id, g] of mapDOM.nodes) {
    g.classList.toggle("is-traced", lit.has(id));
  }
  mapDOM.svg.classList.add("is-tracing");
}

// traceNode lights every connection that touches a node, in both directions.
function traceNode(id) {
  const keys = [];
  const nodes = new Set([id]);
  for (const [key, group] of mapDOM.edges) {
    const { from, to } = group.dataset;
    if (from !== id && to !== id) continue;
    keys.push(key);
    nodes.add(from);
    nodes.add(to);
  }
  traceEdges(keys, [...nodes]);
}

function clearTrace() {
  if (!mapDOM.svg) return;
  mapDOM.svg.classList.remove("is-tracing");
  for (const group of mapDOM.edges.values()) group.classList.remove("is-hot");
  for (const g of mapDOM.nodes.values()) g.classList.remove("is-traced");
}

// MOVE_EPSILON is the distance below which a node is not really moving.
//
// Rows are centred, so adding one node nudges every sibling sideways by a pixel or two.
// Animating that is noise indistinguishable from the old full redraw, so small shifts
// snap and only real moves are allowed to travel.
const MOVE_EPSILON = 3;

function reconcileNodes(rows, pos) {
  const seen = new Set();
  for (const row of rows) {
    for (const n of row.nodes) {
      const p = pos.get(n.id);
      seen.add(n.id);
      let g = mapDOM.nodes.get(n.id);
      if (!g) {
        g = buildMapNode(row.layer, n, p);
        mapDOM.nodes.set(n.id, g);
        mapDOM.svg.append(g);
        g.setAttribute("transform", `translate(${p.x} ${p.y})`);
        g.dataset.x = p.x;
        g.dataset.y = p.y;
        requestAnimationFrame(() => g.classList.remove("is-entering"));
      } else {
        updateMapNode(g, row.layer, n, p);
      }
    }
  }
  for (const [id, g] of mapDOM.nodes) {
    if (seen.has(id)) continue;
    mapDOM.nodes.delete(id);
    fadeOut(g);
  }
}

function buildMapNode(layer, node, p) {
  const g = svgEl("g", {
    class: nodeMapClass(layer, node) + " is-entering",
    tabindex: "0", role: "button", "data-node": node.id,
  });
  g.append(svgEl("rect", {
    x: 0, y: 0, width: p.w, height: MAP.nodeH, rx: 6, class: "map-node-box",
  }));
  if (node.train) {
    // Two lines: the Kubernetes minor, then the train it ships on. The trains are
    // not interchangeable, so they must never read as one node.
    g.append(svgEl("text", {
      x: p.w / 2, y: 13, "text-anchor": "middle", class: "map-node-text",
    }, node.label));
    g.append(svgEl("text", {
      x: p.w / 2, y: 24, "text-anchor": "middle", class: "map-node-train",
    }, node.train));
  } else {
    g.append(svgEl("text", {
      x: p.w / 2, y: MAP.nodeH / 2 + 4, "text-anchor": "middle", class: "map-node-text",
    }, node.label));
  }
  bindMapNode(g, layer, node);
  return g;
}

// bindMapNode wires the handlers once, at creation. They close over the layer and the
// node, so updateMapNode refreshes those bindings whenever the data behind an id changes.
function bindMapNode(g, layer, node) {
  g.onclick = (ev) => {
    if (ev.shiftKey) { togglePeekLock(g, layer, node); return; }
    togglePin(layer.key, node);
  };
  g.onkeydown = (ev) => {
    if (ev.key === "Enter" || ev.key === " ") {
      ev.preventDefault();
      if (ev.shiftKey) togglePeekLock(g, layer, node);
      else togglePin(layer.key, node);
    }
  };
  g.onmouseenter = () => { showPeek(g, layer, node); traceNode(node.id); };
  g.onfocus = () => { showPeek(g, layer, node); traceNode(node.id); };
  g.onmouseleave = () => { hidePeek(); clearTrace(); };
  g.onblur = () => { hidePeek(); clearTrace(); };
}

function updateMapNode(g, layer, node, p) {
  const was = { x: Number(g.dataset.x), y: Number(g.dataset.y) };
  const moved = Math.abs(was.x - p.x) > MOVE_EPSILON || Math.abs(was.y - p.y) > MOVE_EPSILON;
  g.setAttribute("class", nodeMapClass(layer, node) + (moved ? "" : " is-static"));
  g.setAttribute("transform", `translate(${p.x} ${p.y})`);
  g.dataset.x = p.x;
  g.dataset.y = p.y;

  const rect = g.querySelector(".map-node-box");
  if (rect) rect.setAttribute("width", p.w);
  const texts = g.querySelectorAll("text");
  for (const t of texts) t.setAttribute("x", p.w / 2);
  const label = g.querySelector(".map-node-text");
  if (label) label.textContent = node.label;
  const train = g.querySelector(".map-node-train");
  if (train && node.train) train.textContent = node.train;
  bindMapNode(g, layer, node);
}

// fadeOut lets a departing element say goodbye rather than blinking out, then removes it.
// The timeout matches the --step transition in the stylesheet.
function fadeOut(node) {
  node.classList.add("is-leaving");
  setTimeout(() => node.remove(), 220);
}

// edgeProvenance names, in words, why a connection is drawn the way it is.
function edgeProvenance(edge) {
  const pair = edge.releases ? `${edge.releases[0]} × ${edge.releases[1]}` : "";
  const bits = [];
  if (edge.unverified) {
    bits.push(`${pair} was never evaluated upstream — this connection is inferred ` +
      `from the rest of the stack`);
  } else if (edge.untested) {
    bits.push(`${pair}: listed compatible, not tested`);
  } else if (pair) {
    bits.push(`${pair}: verified compatible`);
  }
  if (edge.footnote) bits.push(edge.footnote);
  return bits.join(" — ");
}

function isPinned(layer, node) {
  return !!state.pin &&
    state.pin.product === layer.key &&
    state.pin.version === pinVersionFor(node);
}

function nodeMapClass(layer, node) {
  const classes = ["map-node"];
  if (node.phase && node.phase !== "general") classes.push(`is-${node.phase}`);
  if (node.mixed) classes.push("is-mixed");
  if (node.unverified) classes.push("is-unverified");
  else if (node.untested) classes.push("is-untested");
  if (isPinned(layer, node)) {
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
    const visible = layer.nodes.filter((n) => !(state.hideLegacy && n.legacy));
    const litCount = state.lit
      ? visible.filter((n) => state.lit.has(n.id)).length
      : visible.length;
    const total = visible.length;

    const count = state.lit && litCount !== total
      ? el("div", { class: "layer-count" },
          el("span", { class: "narrowed" }, String(litCount)), ` of ${total}`)
      : el("div", { class: "layer-count" }, `${total}`);

    const nodes = el("div", { class: "layer-nodes" });
    let hidden = 0;
    let filtered = 0;
    for (const node of layer.nodes) {
      if (state.hideLegacy && node.legacy) { hidden++; continue; }
      // The list runs to several dozen entries per layer. Matching on the releases as
      // well as the label means typing a build number finds the line that carries it.
      if (state.filter && !nodeMatches(node, state.filter)) { filtered++; continue; }
      nodes.append(nodeButton(layer, node));
    }
    if (hidden > 0) {
      nodes.append(el("span", { class: "hidden-count" }, `${hidden} legacy hidden`));
    }
    if (filtered > 0) {
      nodes.append(el("span", { class: "hidden-count" }, `${filtered} filtered out`));
    }

    root.append(
      el("div", { class: "layer" },
        el("div", { class: "layer-spine" },
          el("div", { class: "layer-name" }, layer.label), count),
        nodes));
  }
}

// strataKeys moves focus between versions with the arrow keys.
//
// Every node was already focusable and Enter already pinned, but getting from the first
// vCenter release to the twentieth meant twenty Tab presses. Left/Right walk a layer,
// Up/Down cross layers, Home/End jump to the ends, and shift-Enter locks the popover
// open — the keyboard equivalent of shift-click.
function strataKeys(ev, layer, node) {
  if (ev.key === "Enter" && ev.shiftKey) {
    ev.preventDefault();
    togglePeekLock(ev.currentTarget, layer, node);
    return;
  }
  const step = { ArrowRight: 1, ArrowLeft: -1 }[ev.key];
  const cross = { ArrowDown: 1, ArrowUp: -1 }[ev.key];
  if (step === undefined && cross === undefined &&
      ev.key !== "Home" && ev.key !== "End") {
    return;
  }
  ev.preventDefault();

  const rows = [...document.querySelectorAll("#strata .layer-nodes")];
  const row = ev.currentTarget.closest(".layer-nodes");
  const rowIndex = rows.indexOf(row);
  const buttons = [...row.querySelectorAll("button.node")];
  const at = buttons.indexOf(ev.currentTarget);

  if (ev.key === "Home") { buttons[0]?.focus(); return; }
  if (ev.key === "End") { buttons[buttons.length - 1]?.focus(); return; }
  if (step !== undefined) {
    buttons[Math.min(buttons.length - 1, Math.max(0, at + step))]?.focus();
    return;
  }
  // Crossing layers keeps roughly the same position in the row, so moving up and back
  // down lands where you started rather than at the edge.
  const target = rows[rowIndex + cross];
  if (!target) return;
  const peers = [...target.querySelectorAll("button.node")];
  peers[Math.min(peers.length - 1, at)]?.focus();
}

// nodeMatches decides whether a node survives the filter box. Label, train and every
// release it stands for are searched, so "0200" finds the Supervisor builds delivered by
// vCenter 9.1.0.0200 even though no node is labelled that.
function nodeMatches(node, needle) {
  if (node.label.toLowerCase().includes(needle)) return true;
  if (node.train && node.train.toLowerCase().includes(needle)) return true;
  return (node.releases || []).some((r) => r.toLowerCase().includes(needle));
}

function nodeButton(layer, node) {
  const pinned = isPinned(layer, node);
  const lit = state.lit ? state.lit.has(node.id) : false;

  const classes = ["node"];
  if (node.noData) classes.push("nodata");
  if (node.phase && node.phase !== "general") classes.push(`phase-${node.phase}`);
  if (pinned) classes.push("pinned");
  else if (state.lit && lit) classes.push("lit");
  else if (state.lit) classes.push("dim");
  if (!pinned && isRecommended(layer.key, node)) classes.push("picked");

  const label = [node.label];
  if (node.noData) label.push(" · no data yet");

  const button = el("button", {
      class: classes.join(" "),
      type: "button",
      "data-node": node.id,
      "aria-pressed": pinned ? "true" : "false",
      // Shift-click keeps the popover open instead of pinning: its contents — the exact
      // builds, the caveats, the upstream notes — are worth reading and copying, which a
      // popover that dies the moment the pointer moves does not allow.
      onclick: (ev) => {
        if (ev.shiftKey) { togglePeekLock(ev.currentTarget, layer, node); return; }
        togglePin(layer.key, node);
      },
      onmouseenter: (ev) => showPeek(ev.currentTarget, layer, node),
      onfocus: (ev) => showPeek(ev.currentTarget, layer, node),
      onmouseleave: () => hidePeek(),
      onblur: () => hidePeek(),
      onkeydown: (ev) => strataKeys(ev, layer, node),
    },
    label.join(""),
    node.train ? el("span", { class: "train" }, node.train) : null,
    node.legacy ? el("span", { class: `phase-tag phase-${node.phase}` },
      node.phase === "end-of-support" ? "EOS" : "TG") : null,
    // A line keeps its best phase as its headline, but a green node holding End of
    // Support builds has to admit it, or picking one out of it is a coin toss.
    !node.legacy && node.mixed
      ? el("span", { class: `phase-tag phase-${node.worstPhase} is-mixed` },
          node.worstPhase === "end-of-support" ? "some EOS" : "some TG")
      : null,
    lit && node.unverified ? el("span", { class: "prov-tag is-unverified" }, "inferred") : null,
    lit && !node.unverified && node.untested
      ? el("span", { class: "prov-tag is-untested" }, "not tested") : null,
    node.footnotes?.length ? el("span", { class: "prov-tag is-footnote" }, "note") : null,
    node.detail
      ? el("span", { class: "detail" },
          layer.key === "vcenter" ? `on ESX ${node.detail}` : node.detail)
      : null);

  return button;
}

// A node stands for one or more releases, and pinning selects one of them: the newest,
// which the server names in `pin`. It is sent verbatim — never a display string, which is
// what used to happen and made every grouped Supervisor node unclickable.
function pinVersionFor(node) {
  return node.pin || node.label;
}

function isRecommended(layerKey, node) {
  const chosen = state.recommended?.[layerKey];
  return !!chosen && node.releases?.includes(chosen.version);
}

function togglePin(layerKey, node) {
  const version = pinVersionFor(node);
  const same = state.pin && state.pin.product === layerKey && state.pin.version === version;
  setPin(same ? null : { product: layerKey, version });
}

// setPin is the single door every selection change goes through — clicks, Esc, Clear,
// the error recovery button, and the back button. Anything that mutates state.pin
// elsewhere would silently desynchronise the URL from the picture.
function setPin(pin, { push = true } = {}) {
  state.pin = pin;
  hidePeek();
  syncURL(push);
  loadStack();
}

// --- the URL is the state --------------------------------------------------
//
// The pin used to live only in memory: a reload lost it, a link could not carry it, and
// the back button did nothing. Putting it in the query string makes a selection something
// you can bookmark, send to a colleague on the shared instance, or paste into a change
// ticket — and it makes the browser's own history the undo stack, so no custom one is
// needed.
//
// The keys are the ones the API already speaks (product, version), so the address bar and
// /api/stackmap read the same. Defaults are omitted: a bare URL means the default view.

const URL_DEFAULTS = { legacy: true };

function currentURL() {
  const params = new URLSearchParams();
  if (state.pin) {
    params.set("product", state.pin.product);
    params.set("version", state.pin.version);
  }
  if (state.hideLegacy !== URL_DEFAULTS.legacy) params.set("legacy", "1");
  const query = params.toString();
  return location.pathname + (query ? "?" + query : "");
}

// syncURL writes the current selection into the address bar. Pin changes push a history
// entry so back walks the exploration; view-only changes replace, so the filter checkbox
// does not fill the back button with noise.
let restoring = false;
function syncURL(push) {
  if (restoring) return;
  const url = currentURL();
  if (url === location.pathname + location.search) return;
  if (push) history.pushState(readState(), "", url);
  else history.replaceState(readState(), "", url);
}

function readState() {
  return { pin: state.pin, hideLegacy: state.hideLegacy };
}

// applyURL seeds the app from the address bar. Returns whether the URL asked for
// anything, so boot() knows whether it may fall back to its own defaults.
function applyURL() {
  const params = new URLSearchParams(location.search);
  const product = params.get("product");
  const version = params.get("version");
  state.hideLegacy = params.get("legacy") !== "1";
  state.pin = product && version ? { product, version } : null;
  $("#hide-legacy").checked = state.hideLegacy;
  return params.has("product") || params.has("legacy");
}

window.addEventListener("popstate", () => {
  restoring = true;
  applyURL();
  loadStack().finally(() => { restoring = false; });
});

function renderPinControls() {
  const label = $("#pin-label");
  const saved = myStack();
  const isSaved = !!saved && !!state.pin &&
    saved.product === state.pin.product && saved.version === state.pin.version;

  if (state.pin) {
    const layer = state.layers.find((l) => l.key === state.pin.product);
    label.textContent = `${layer ? layer.label : state.pin.product} ${state.pin.version}`;
  } else {
    label.textContent = "";
  }
  for (const [sel, on] of [["#clear-pin", !!state.pin], ["#copy-link", true],
                           ["#save-stack", !!state.pin]]) {
    $(sel).classList.toggle("hidden", !on);
  }
  $("#save-stack").textContent = isSaved ? "Forget my stack" : "Save as my stack";
  $("#save-stack").title = isSaved
    ? "Stop opening on this selection"
    : "Open on this selection in this browser from now on";
}

// copyLink hands over the address of exactly what is on screen. The address bar already
// carries it, but nobody thinks to copy the address bar, and the point of putting the
// selection in the URL is that a decision can travel.
async function copyLink() {
  const url = location.origin + currentURL();
  await copyText(url, $("#copy-link"), "Link copied");
}

// copyText is the one clipboard path, with a fallback for the plain-http case: the
// Clipboard API is unavailable on a shared instance served over http to a hostname, which
// is exactly how this tool is deployed.
async function copyText(text, button, done) {
  let ok = false;
  try {
    await navigator.clipboard.writeText(text);
    ok = true;
  } catch {
    const box = el("textarea", { class: "offscreen" });
    box.value = text;
    document.body.append(box);
    box.select();
    try { ok = document.execCommand("copy"); } catch { ok = false; }
    box.remove();
  }
  if (!button) return ok;
  const was = button.textContent;
  button.textContent = ok ? done : "Copy failed";
  setTimeout(() => { button.textContent = was; }, 1600);
  return ok;
}

// renderMeta says which snapshot the page is reading, and whether it is still moving.
//
// "6h old" cannot tell one missed tick from two days of failures, and on a shared
// instance the only person who could see the difference was whoever can read the logs.
// ageHours is computed when the response is built, which is the same instant it is read
// on a live server and days earlier on a static build. Deriving it from fetchedAt keeps a
// published snapshot honest about its age instead of freezing it at the moment of the
// build. The server's own figure is left in the payload for programs reading the API.
function ageHours(meta) {
  const fetched = Date.parse(meta.fetchedAt);
  if (Number.isNaN(fetched)) return meta.ageHours;
  return Math.max(0, Math.floor((Date.now() - fetched) / 3600000));
}

function renderMeta(meta) {
  const mode = meta.refreshInterval
    ? `refreshes every ${meta.refreshInterval}`
    : (meta.readOnly ? "read-only" : "refresh on demand");
  const box = $("#meta");
  box.textContent = "";
  box.append(el("span", {},
    `${meta.fetchedAt} · ${ageHours(meta)}h old · ${mode} · ` +
    meta.products.map((p) => `${p.label} ${p.count}`).join("  ") +
    (meta.version ? ` · vkstack ${meta.version}` : "")));

  const r = meta.refresh;
  if (!r || !r.attempts?.length) return;
  if (r.consecutiveFailures > 0) {
    box.append(el("button", {
      class: "stale-flag",
      type: "button",
      onclick: () => $("#refresh-log").classList.toggle("hidden"),
    }, `${r.consecutiveFailures} failed refresh${r.consecutiveFailures > 1 ? "es" : ""} in a row`));
  } else {
    box.append(el("button", {
      class: "refresh-flag",
      type: "button",
      onclick: () => $("#refresh-log").classList.toggle("hidden"),
    }, "refresh log"));
  }

  const log = $("#refresh-log");
  log.textContent = "";
  log.append(el("h2", {}, "Background refresh attempts"));
  const list = el("ul");
  for (const a of r.attempts) {
    list.append(el("li", { class: a.ok ? "ok" : "failed" },
      el("span", { class: "when" }, a.at),
      a.ok ? "succeeded" : `failed — ${a.error}`));
  }
  log.append(list);
  if (r.consecutiveFailures > 0) {
    log.append(el("p", { class: "note" },
      "The cache keeps serving what it already had; stale data beats no data. " +
      "Everything on screen is from the snapshot named above."));
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
  let hiddenByFilter = 0;
  // Bottom-up, matching the map above it.
  for (const key of ["vcenter", "esx", "supervisor", "vks", "vkr"]) {
    const pick = state.recommended[key];
    if (!pick) continue;
    // The legacy filter is a view setting; the solver still reaches past it. A
    // recommendation the map is hiding has to say so rather than read as ordinary.
    const hidden = pick.legacy && state.hideLegacy;
    if (hidden) hiddenByFilter++;
    dl.append(
      el("dt", {}, labelFor(key)),
      el("dd", { class: pick.legacy ? `phase-${pick.phase}` : null },
        pick.version,
        pick.legacy
          ? el("span", { class: `phase-tag phase-${pick.phase}` },
              pick.phase === "end-of-support" ? "EOS" : "TG")
          : null,
        hidden ? el("span", { class: "note" }, "hidden by the legacy filter") : null));
  }
  box.append(el("h2", {}, "Newest working stack from here"), dl);

  if (hiddenByFilter > 0) {
    box.append(el("p", { class: "note" },
      `This stack uses ${hiddenByFilter} release${hiddenByFilter > 1 ? "s" : ""} the ` +
      `map is hiding: "Hide legacy releases" filters the picture, not the solver.`));
  }
  const p = state.provenance;
  if (p) {
    box.append(el("p", { class: "provenance-summary" }, provenanceSentence(p)));
    for (const note of p.footnotes || []) {
      box.append(el("p", { class: "note footnote-line" }, note));
    }
  }
  renderManifestActions(box);
}

// --- the handoff record ----------------------------------------------------
//
// What this tool produces is a decision that has to survive leaving the screen: an
// engineer picks a stack, justifies it to a change board, writes it into a maintenance
// plan, and hands the versions to whoever executes them — possibly weeks later, possibly
// someone else. Until now the web UI ended at pixels while the CLI could emit JSON.
//
// One record, two readings. The approver wants the reasoning; the person doing the work
// at 2am wants an ordered checklist. Both carry the exact release strings, the support
// phase of each, the provenance, the upstream caveats, and the snapshot they came from —
// without which a stack that was true in August gets executed in October.

// UPGRADE_ORDER mirrors model.UpgradeOrder: vCenter must be at or ahead of the hosts and
// the Supervisor it manages, so it moves first; the guest clusters move last.
const UPGRADE_ORDER = ["vcenter", "esx", "supervisor", "vks", "vkr"];

function buildManifest() {
  if (!state.recommended) return null;
  const steps = [];
  for (const key of UPGRADE_ORDER) {
    const pick = state.recommended[key];
    if (!pick) continue;
    steps.push({
      layer: key,
      label: labelFor(key),
      version: pick.version,
      phase: pick.phase,
      phaseLabel: pick.phaseLabel,
      legacy: !!pick.legacy,
      pinned: !!state.pin && state.pin.product === key,
      why: EDGE_PROSE[key] || "",
    });
  }
  return {
    generated: new Date().toISOString(),
    selection: state.pin ? `${labelFor(state.pin.product)} ${state.pin.version}` : "none",
    snapshot: state.meta
      // Age as it stands now, not as the response was stamped: this line is read back
      // as "h old when this was generated", and on a static build those differ.
      ? { fetchedAt: state.meta.fetchedAt, ageHours: ageHours(state.meta), vkstack: state.meta.version }
      : null,
    steps,
    provenance: state.provenance,
    link: location.origin + currentURL(),
  };
}

// EDGE_PROSE is the one-line reason each hop sits where it does in the order. It repeats
// what the model view already says, because a record read weeks later by someone who
// never opened this tool cannot follow a link to find out.
const EDGE_PROSE = {
  vcenter: "moves first — it must be at or ahead of the hosts and the Supervisor it manages",
  esx: "follows vCenter — the hosts the Supervisor runs on",
  supervisor: "delivered and managed by vCenter, running on those hosts",
  vks: "runs on the Supervisor and provisions the guest clusters",
  vkr: "the guest cluster release VKS provisions — moves last",
};

// manifestMarkdown renders the record. `audience` picks the reading: "approver" keeps the
// reasoning and the caveats, "executor" strips to the ordered steps someone works
// through. Same data, so the two can never drift apart.
function manifestMarkdown(m, audience) {
  const lines = [];
  const stamp = m.snapshot
    ? `${m.snapshot.fetchedAt} (${m.snapshot.ageHours}h old when this was generated)`
    : "unknown";

  if (audience === "executor") {
    lines.push(`# Upgrade steps — in this order`, "");
    lines.push(`Selection: ${m.selection}`, "");
    m.steps.forEach((s, i) => {
      lines.push(`${i + 1}. [ ] **${s.label}** \`${s.version}\`` +
        (s.legacy ? `  ⚠ ${s.phaseLabel}` : "") +
        (s.pinned ? "  _(the version you pinned)_" : ""));
    });
    lines.push("", `Do not reorder. ${EDGE_PROSE.vcenter[0].toUpperCase()}` +
      EDGE_PROSE.vcenter.slice(1) + ".", "");
  } else {
    lines.push(`# Target stack`, "");
    lines.push(`Selection: ${m.selection}`, "");
    lines.push("| Order | Component | Version | Support phase | Why here |");
    lines.push("|---|---|---|---|---|");
    m.steps.forEach((s, i) => {
      lines.push(`| ${i + 1} | ${s.label} | \`${s.version}\` | ${s.phaseLabel} | ${s.why} |`);
    });
    lines.push("");
    if (m.provenance) {
      lines.push(`**Evidence.** ${provenanceSentence(m.provenance)}.`, "");
      for (const note of m.provenance.footnotes || []) lines.push(`- Upstream note — ${note}`);
      if (m.provenance.footnotes?.length) lines.push("");
    }
  }

  lines.push(`**Source.** Broadcom product interoperability matrix, snapshot ${stamp}.`);
  lines.push("");
  lines.push("This is a valid *destination*, not an upgrade path: the matrix says which " +
    "versions may coexist, not whether a given hop is supported as an upgrade. Confirm " +
    "against the product upgrade documentation before executing.");
  lines.push("");
  lines.push(`Recheck before executing — upstream data moves. ${m.link}`);
  return lines.join("\n");
}

function renderManifestActions(box) {
  const actions = el("div", { class: "manifest-actions" },
    el("button", {
      class: "ghost", type: "button",
      title: "One line for a ticket or a chat message",
      onclick: (ev) => {
        const m = buildManifest();
        const line = m.steps.map((s) => `${s.label} ${s.version}`).join(" · ");
        copyText(`${line} — verified against the interop matrix ` +
          `(${m.snapshot ? m.snapshot.fetchedAt : "unknown snapshot"}) ${m.link}`,
          ev.currentTarget, "Copied");
      },
    }, "Copy one line"),
    el("button", {
      class: "ghost", type: "button",
      title: "The stack with its evidence, for a change ticket",
      onclick: (ev) => copyText(manifestMarkdown(buildManifest(), "approver"),
        ev.currentTarget, "Copied"),
    }, "Copy for the change ticket"),
    el("button", {
      class: "ghost", type: "button",
      title: "Ordered checklist for whoever runs the upgrade",
      onclick: (ev) => copyText(manifestMarkdown(buildManifest(), "executor"),
        ev.currentTarget, "Copied"),
    }, "Copy the run order"),
    el("button", {
      class: "ghost", type: "button",
      onclick: () => downloadManifest(),
    }, "Download .md"));
  box.append(actions);
}

function downloadManifest() {
  const m = buildManifest();
  if (!m) return;
  const text = manifestMarkdown(m, "approver") + "\n\n---\n\n" +
    manifestMarkdown(m, "executor");
  const blob = new Blob([text], { type: "text/markdown" });
  const url = URL.createObjectURL(blob);
  const a = el("a", { href: url, download: `vkstack-${m.steps[0]?.version || "stack"}.md` });
  document.body.append(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// One sentence on how much of the shown stack is a lookup and how much is trust. The
// matrix's own hedges — "compatible, not tested", pairs it never evaluated — are the
// part a compatibility tool must not quietly absorb.
function provenanceSentence(p) {
  const bits = [`${p.verified} of ${p.pairs} dependency pairs verified compatible`];
  if (p.untested > 0) {
    bits.push(`${p.untested} listed compatible but not tested (${p.untestedPairs.join(", ")})`);
  }
  if (p.inferredPairs?.length) {
    bits.push(`${p.inferredPairs.length} never evaluated upstream, taken on trust ` +
      `(${p.inferredPairs.join(", ")})`);
  }
  return bits.join(" · ");
}

function labelFor(key) {
  const known = { vcenter: "vCenter", esx: "ESX", supervisor: "Supervisor", vks: "VKS", vkr: "VKr" };
  return known[key] || key;
}

// --- peek popover ----------------------------------------------------------

// peekLock holds the popover open when the reader asked for it, rather than only while
// the pointer is perfectly still. The popover now carries the exact release list,
// provenance and upstream footnotes — content worth reading and copying, which a
// hover-only, pointer-events:none tooltip makes impossible.
let peekLock = null;

function showPeek(anchor, layer, node) {
  if (peekLock && peekLock !== node.id) return; // a locked popover is not overwritten
  const peek = $("#peek");
  peek.textContent = "";

  // What the node means comes first and is never truncated; the release list is what
  // gets cut when it is long. Putting provenance after the list buried the one part a
  // reader cannot reconstruct for themselves.
  const head = [];
  if (layer.key === "vcenter" && node.hosts?.length) {
    head.push(`Runs on ESX: ${node.hosts.join(", ")}`);
  } else if (layer.key === "vcenter" && state.lit?.has(node.id)) {
    head.push("No ESX release can carry this vCenter and the current selection.");
  }
  if (node.train) {
    head.push(node.train === "vsc9"
      ? "Train vsc9 — these versions ship with vCenter 9.x"
      : `Train ${node.train} — numbered separately from vCenter`);
  }
  if (node.legacy) {
    head.push(`${node.phaseLabel} — this is what the interop site's ` +
      `"hide legacy releases" checkbox removes.`);
  } else if (node.mixed) {
    head.push(`${node.phaseLabel}, but not every build in this line: the oldest is ` +
      `${node.worstPhaseLabel}.`);
  }
  // Where the warrant is weaker than a tested lookup, name the pair. "Not tested" with
  // no pair attached reads as a claim about this product when it may be about another.
  for (const line of node.provenance || []) head.push(line);
  for (const note of node.footnotes || []) head.push(`Upstream note — ${note}`);

  // Notes are the releases written for a reader; releases are the raw strings. Either
  // way, say which one a click selects rather than leaving "the newest" to be assumed.
  const body = [];
  const notes = node.notes?.length ? node.notes : (node.releases || []);
  if (node.noData) {
    head.push("Upstream publishes no compatibility data for this release yet.");
  } else if (notes.length > 1) {
    head.push(`Clicking selects ${node.pin} — the newest of ${notes.length} here.`);
    body.push(...notes);
  } else if (notes.length === 1 && notes[0] !== node.label) {
    body.push(notes[0]);
  }
  if (!head.length && !body.length) return;

  const locked = peekLock === node.id;
  // Locked, the whole list is shown and stays put; hovering, it is capped so the popover
  // does not swallow the page.
  const room = locked ? body.length : Math.max(0, 16 - head.length);
  peek.append(el("div", { class: "peek-title" },
    `${layer.label} ${node.label}`,
    el("span", { class: "peek-hint" },
      locked ? "Esc or shift-click to release" : "shift-click to keep open")));
  for (const line of head) peek.append(el("div", { class: "peek-head" }, line));
  for (const line of body.slice(0, room)) peek.append(el("div", {}, line));
  if (body.length > room) peek.append(el("div", {}, `+${body.length - room} more`));
  if (locked) {
    peek.append(el("div", { class: "peek-actions" },
      el("button", {
        class: "ghost", type: "button",
        onclick: (ev) => copyText([...body].join("\n"), ev.currentTarget, "Copied"),
      }, "Copy releases")));
  }

  peek.classList.toggle("is-locked", locked);
  peek.classList.remove("hidden");
  const box = anchor.getBoundingClientRect();
  const size = peek.getBoundingClientRect();
  peek.style.top = `${peekTop(anchor, box, size)}px`;
  peek.style.left = `${Math.max(8, Math.min(box.left, window.innerWidth - size.width - 8))}px`;
}

// peekTop keeps the popover off the thing it is describing.
//
// Over the map, hovering now traces a connection, and a popover floating above the node
// lands squarely on the layers above it — hiding the lines it was opened to explain. So a
// map node parks its popover clear of the whole frame, below it by preference and above
// only when the viewport leaves no room. A node in the version lists has no drawing to
// protect and keeps the tighter placement: above, flipping below when it would clip.
function peekTop(anchor, box, size) {
  const frame = anchor.ownerSVGElement && $(".map-frame")?.getBoundingClientRect();
  if (frame) {
    const below = frame.bottom + 8;
    if (below + size.height <= window.innerHeight - 8) return below;
    return Math.max(8, frame.top - size.height - 8);
  }
  const above = box.top - size.height - 8;
  return above < 8 ? box.bottom + 8 : above;
}

// togglePeekLock is bound to a modified click on a node: keep this popover open, with its
// full release list and a copy button, until it is dismissed. A plain click still pins.
function togglePeekLock(anchor, layer, node) {
  peekLock = peekLock === node.id ? null : node.id;
  if (peekLock) showPeek(anchor, layer, node);
  else hidePeek(true);
}

function hidePeek(force = false) {
  if (peekLock && !force) return;
  peekLock = null;
  const peek = $("#peek");
  peek.classList.add("hidden");
  peek.classList.remove("is-locked");
}

// --- boot -------------------------------------------------------------------

// MY_STACK holds what this browser actually runs, if its owner said so. It is a personal
// default, not part of any link: it decides where a bare visit lands, and a URL naming a
// selection always wins over it.
const MY_STACK = "vkstack.myStack";

function myStack() {
  try {
    const raw = localStorage.getItem(MY_STACK);
    const pin = raw ? JSON.parse(raw) : null;
    return pin && pin.product && pin.version ? pin : null;
  } catch {
    return null;
  }
}

function setMyStack(pin) {
  if (pin) localStorage.setItem(MY_STACK, JSON.stringify(pin));
  else localStorage.removeItem(MY_STACK);
  renderPinControls();
}

async function boot() {
  try {
    const meta = await api("/api/meta");
    // Kept, not just printed: the manifest has to stamp which snapshot it came from.
    state.meta = meta;
    renderMeta(meta);
  } catch (err) {
    $("#meta").textContent = err.message;
  }

  // The URL is a more specific instruction than any default, so it is read first. Then
  // this browser's own stack, if one was saved. Only failing both does the map fall back
  // to the newest base version, so an arrival is never an empty frame.
  const fromURL = applyURL();
  if (!state.pin && !fromURL) state.pin = myStack();

  await loadStack();
  if (!state.pin && !fromURL) {
    const base = state.layers.find((l) => l.key === "vcenter");
    const newest = base?.nodes.find((n) => !n.noData);
    if (newest) {
      state.pin = { product: "vcenter", version: pinVersionFor(newest) };
      await loadStack();
    }
  }
  // Make a bare visit shareable retroactively, without adding a phantom history entry.
  syncURL(false);
}

$("#clear-pin").addEventListener("click", () => setPin(null));
$("#copy-link").addEventListener("click", copyLink);
$("#save-stack").addEventListener("click", () => {
  setMyStack(myStack() && state.pin && myStack().version === state.pin.version
    ? null
    : state.pin);
});
$("#hide-legacy").addEventListener("change", (ev) => {
  state.hideLegacy = ev.target.checked;
  syncURL(false);
  renderMap();
  renderStrata();
});
$("#filter").addEventListener("input", (ev) => {
  state.filter = ev.target.value.trim().toLowerCase();
  renderStrata();
});
document.addEventListener("keydown", (ev) => {
  if (ev.key === "Escape") {
    if (!$("#peek").classList.contains("hidden")) { hidePeek(true); return; }
    if (state.pin) setPin(null);
  }
});
window.addEventListener("scroll", () => hidePeek(), { passive: true });

boot();
