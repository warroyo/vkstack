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
  view: "stack",      // which tab is showing
  deadEnd: null,      // why the current selection reaches no stack, when it does not
  // Optional layers the reader has opened, by product key. NSX and Avi are not in every
  // deployment, so they start closed and the core stack still reads as four rows.
  //
  // They are independent of each other throughout: opening NSX says nothing about Avi,
  // and Avi in front of a distributed switch with no NSX anywhere is an ordinary
  // deployment. Nothing here may ever open one because the other is open.
  include: [],
  // The vSphere platform generation on show, as a vCenter major version. 0 is every
  // generation. Unlike hideLegacy and filter below it, this is not a view filter: it is a
  // load-time one, so changing it re-asks the server.
  //
  // A generation constrains vCenter and nothing else. ESX 8.x, NSX 4.x, Avi 31.x and the
  // Supervisor vsc0 train all pair with vCenter 9 in the published matrix, so they stay on
  // offer under it — filtering them by their own version number would hide compatibility
  // upstream actually publishes.
  generation: 0,
  generations: [],    // the generations the server offers, for the tabs
};

const isOpen = (layer) => !layer.optional || state.include.includes(layer.key);

// openLayer opens one optional layer, leaving every other layer as it was.
function openLayer(key) {
  if (state.include.includes(key)) return false;
  state.include = [...state.include, key];
  return true;
}

// OPTIONAL_ORDER canonicalises the `with` parameter.
//
// The value is a cache key on a static build, where every answer was enumerated ahead of
// time, so "nsx,avi" and "avi,nsx" have to be the same string or half the lookups miss.
// This is the model's own product order.
const OPTIONAL_ORDER = ["nsx", "avi"];
const withParam = () =>
  OPTIONAL_ORDER.filter((k) => state.include.includes(k)).join(",");

// genParam is the generation as the API and the bundle names spell it: "" for all.
const genParam = () => (state.generation ? String(state.generation) : "");

// GENERATION_LABELS name the tabs. The server sends the list of majors; only the wording
// lives here, and an unlisted major still gets a usable tab.
const generationLabel = (gen) => (gen ? `vSphere ${gen}` : "All");

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
// One bundle per set of open optional layers per generation, fetched on demand and kept
// once fetched.
//
// Opening NSX or Avi changes every answer on the map, and so does picking a generation, so
// the states cannot share a file. Splitting them means the first load carries only the
// plain map: `layers` is three quarters of the bytes, most readers never open either
// layer, and nobody reads two generations at once.
const bundles = new Map();

function bundleName(withKey, genKey) {
  let name = "data";
  if (withKey) name += `-${withKey.replace(/,/g, "-")}`;
  if (genKey) name += `-gen${genKey}`;
  return `${name}.json`;
}

function siteData(withKey = "", genKey = "") {
  const cacheKey = `${withKey}|${genKey}`;
  if (!bundles.has(cacheKey)) {
    const name = bundleName(withKey, genKey);
    bundles.set(cacheKey, fetch(name).then((res) => {
      if (!res.ok) throw new Error(`could not load the site data (${name}: ${res.status})`);
      return res.json();
    }));
  }
  return bundles.get(cacheKey);
}

async function apiStatic(path) {
  const url = new URL(path, location.href);
  // The generation picks the bundle just as `with` does: it is a load-time filter, so a
  // vSphere 9 map is a different set of answers rather than a subset of one.
  const genKey = url.searchParams.get("gen") || "";

  switch (url.pathname) {
    case "/api/meta":
      // meta and the default map live only in each generation's base bundle; the variants
      // carry nothing the page needs before a layer is opened.
      return (await siteData("", genKey)).meta;
    case "/api/stackmap": {
      const product = url.searchParams.get("product");
      const version = url.searchParams.get("version");
      // Which optional layers are open picks the bundle, because it changes the whole
      // answer — which nodes are lit and how each is narrowed, not only the recommendation.
      const withKey = url.searchParams.get("with") || "";
      const data = await siteData(withKey, genKey);

      const answer = !product || !version
        ? data.stackmaps?.[""]?.[""]
        : data.stackmaps?.[product]?.[version];
      // Only reachable if the bundle and the UI disagree about what is clickable, which
      // means the build is stale rather than the selection being wrong.
      if (!answer) {
        if (!product || !version) return (await siteData("", genKey)).stackmap;
        throw new Error(`this build has no answer for ${product} ${version}` +
          (withKey ? ` with ${withKey}` : "") + (genKey ? ` under vSphere ${genKey}` : ""));
      }
      return answer;
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
  const params = new URLSearchParams();
  if (state.pin) {
    params.set("product", state.pin.product);
    params.set("version", state.pin.version);
  }
  // The server needs to know which optional layers are open: a reader who opened the NSX
  // row is asking which NSX version goes with their selection, and the recommended stack
  // has to answer that rather than stay silent about it.
  if (withParam()) params.set("with", withParam());
  // The generation has to reach the solver, not just the renderer: under vSphere 9 the
  // lit set still includes ESX 8.x, which hiding 8.x in the browser would wrongly drop.
  if (genParam()) params.set("gen", genParam());
  const query = params.toString();
  const url = "/api/stackmap" + (query ? "?" + query : "");

  const ticket = ++inFlight;
  // The previous answer stays on screen, dimmed, until this one lands. A blank frame
  // during a slow round trip reads as a broken tool rather than as work in progress.
  $("#map").classList.add("is-solving");

  let data;
  try {
    data = await api(url);
  } catch (err) {
    if (ticket !== inFlight) return false;
    $("#map").classList.remove("is-solving");
    showStackError(err.message);
    return false;
  }
  if (ticket !== inFlight) return false; // a newer click has already been asked
  $("#map").classList.remove("is-solving");
  $("#stack-error").classList.add("hidden");

  state.layers = data.layers;
  // A pin on an optional layer is its own opt-in, and a saved link can carry one without
  // `with`. Open that layer — and only that one — and ask again.
  //
  // The reload is not optional. Which layers are open changes the whole answer, so the
  // response in hand was solved for a map that did not include this row: it lit no nodes
  // in it and drew no edges to the pinned node. Rendering that put the reader's own
  // selection on screen connected to nothing.
  const pinnedLayer = state.layers.find((l) => l.key === state.pin?.product);
  if (pinnedLayer?.optional && openLayer(pinnedLayer.key)) {
    syncURL(false);
    return loadStack();
  }

  state.lit = data.lit ? new Set(data.lit) : null;
  state.recommended = data.recommended || null;
  state.provenance = data.provenance || null;
  state.edges = data.edges || [];
  state.deadEnd = data.deadEnd || null;

  renderMap();
  renderStrata();
  renderPinControls();
  renderRecommended();
  // Reported so a caller that changed a load-time filter can tell a solve that failed
  // because of it from one that succeeded. setGeneration is the only such caller.
  return true;
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

// --- the viewport -----------------------------------------------------------
//
// The layout above computes one true set of positions, in world coordinates, and never
// thinks about the window. This decides how much of that drawing is on screen.
//
// It exists because the widest row is as wide as the number of versions in it: unfiltered,
// the vCenter row is 22 nodes and about 2000px, against a map column nearer 1000px beside
// the rail. That used to be absorbed by `overflow-x: auto`, which scrolls but cannot show
// you the shape of the thing — and the shape is the answer.
//
// k is scale: 1 means one world unit per CSS pixel, which is what the layout was tuned
// for. x and y are the world coordinates of the viewport's top-left corner.
const view = { x: 0, y: 0, k: 1, dirty: false };

// world is the last laid-out size, kept so the wheel, the buttons and a window resize can
// all recompute the viewBox without re-running the layout.
let world = { w: 0, h: 0 };

const ZOOM_MAX = 3;
const ZOOM_STEP = 1.2;

const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));

// fitScale is the scale at which the whole drawing fits the frame's width. Capped at 1:
// a map narrower than its frame is drawn at its natural size and centred, never blown up.
function fitScale() {
  const frameW = $("#map").clientWidth;
  if (!frameW || !world.w) return 1;
  return Math.min(1, frameW / world.w);
}

// Zooming out past the fit would only add empty space around a map already fully visible.
const minScale = () => Math.min(1, fitScale());

// applyView writes the current viewport onto the SVG.
//
// The element's own height shrinks with the scale so a fitted map does not sit in a box
// sized for a drawing three times larger, but never grows past the natural height: zooming
// in reveals less of the map rather than making the page taller.
function applyView() {
  if (!mapDOM.svg || !world.w) return;
  const frameW = $("#map").clientWidth || world.w;

  view.k = clamp(view.k, minScale(), ZOOM_MAX);
  const viewportH = world.h * Math.min(1, view.k);
  const boxW = frameW / view.k;
  const boxH = viewportH / view.k;

  // Clamped so panning cannot wander off the drawing. When the viewport is larger than
  // the world on an axis, the world is centred on it instead.
  view.x = boxW >= world.w ? (world.w - boxW) / 2 : clamp(view.x, 0, world.w - boxW);
  view.y = boxH >= world.h ? (world.h - boxH) / 2 : clamp(view.y, 0, world.h - boxH);

  mapDOM.svg.setAttribute("viewBox", `${view.x} ${view.y} ${boxW} ${boxH}`);
  mapDOM.svg.setAttribute("height", viewportH);
  mapDOM.svg.classList.toggle("is-pannable", boxW < world.w || boxH < world.h);
  renderMapControls();
}

// fitView shows the whole drawing. Called on every render until the reader takes over.
function fitView() {
  view.k = fitScale();
  view.x = 0;
  view.y = 0;
  applyView();
}

// resetView returns to 1:1, which is the size every label was measured for.
function resetView() {
  view.k = 1;
  view.dirty = false;
  view.x = 0;
  view.y = 0;
  applyView();
}

// zoomAbout scales around a fixed point in world coordinates, so the thing under the
// pointer stays under the pointer.
function zoomAbout(worldX, worldY, factor) {
  const frameW = $("#map").clientWidth || world.w;
  const before = { w: frameW / view.k, h: (world.h * Math.min(1, view.k)) / view.k };
  view.k = clamp(view.k * factor, minScale(), ZOOM_MAX);
  const after = { w: frameW / view.k, h: (world.h * Math.min(1, view.k)) / view.k };

  // Keep the fraction of the way across the viewport that the point sat at.
  const fx = before.w ? (worldX - view.x) / before.w : 0.5;
  const fy = before.h ? (worldY - view.y) / before.h : 0.5;
  view.x = worldX - fx * after.w;
  view.y = worldY - fy * after.h;
  view.dirty = true;
  applyView();
}

// zoomBy zooms about the middle of the viewport, for the buttons and the keyboard.
function zoomBy(factor) {
  const frameW = $("#map").clientWidth || world.w;
  const boxW = frameW / view.k;
  const boxH = (world.h * Math.min(1, view.k)) / view.k;
  zoomAbout(view.x + boxW / 2, view.y + boxH / 2, factor);
}

// pointToWorld converts a pointer position into world coordinates through the SVG's own
// transform, so it stays correct whatever the viewBox currently is.
function pointToWorld(ev) {
  const svg = mapDOM.svg;
  const ctm = svg?.getScreenCTM();
  if (!ctm) return null;
  const pt = new DOMPoint(ev.clientX, ev.clientY).matrixTransform(ctm.inverse());
  return { x: pt.x, y: pt.y };
}

// panIntoView scrolls a world-space box into the viewport. Tabbing between nodes relies
// on it: a viewBox is not a scroll offset, so the browser's own scroll-into-view has
// nothing to move, and a focused node off the left edge would simply never be seen.
function panIntoView(box) {
  if (!mapDOM.svg || !world.w) return;
  const frameW = $("#map").clientWidth || world.w;
  const boxW = frameW / view.k;
  const boxH = (world.h * Math.min(1, view.k)) / view.k;
  const pad = 24;

  let { x, y } = view;
  if (box.x - pad < x) x = box.x - pad;
  else if (box.x + box.w + pad > x + boxW) x = box.x + box.w + pad - boxW;
  if (box.y - pad < y) y = box.y - pad;
  else if (box.y + box.h + pad > y + boxH) y = box.y + box.h + pad - boxH;

  if (x === view.x && y === view.y) return;
  view.x = x;
  view.y = y;
  applyView();
}

function renderMapControls() {
  const controls = $("#map-controls");
  if (!controls) return;
  const showing = !!mapDOM.svg && world.w > 0;
  controls.hidden = !showing;
  if (!showing) return;
  $("#map-zoom-level").textContent = `${Math.round(view.k * 100)}%`;
  $("#map-zoom-in").disabled = view.k >= ZOOM_MAX - 0.001;
  $("#map-zoom-out").disabled = view.k <= minScale() + 0.001;
}

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
  // There is no drawing to zoom, so the controls go with it — and the next map that
  // arrives should fit itself rather than inherit a zoom set on a different drawing.
  world = { w: 0, h: 0 };
  view.dirty = false;
  renderMapControls();
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
  //
  // A closed optional layer is not drawn at all. It is not part of the stack the reader
  // is looking at, and a dark row for something they do not run is noise.
  const rows = state.layers.filter(isOpen).map((layer) => ({
    layer,
    nodes: layer.nodes.filter((n) =>
      state.lit?.has(n.id) &&
      (!(state.hideLegacy && n.legacy) || isPinned(layer, n))),
  }));

  // A pin with no complete stack is not the same as no pin. Both used to draw nothing.
  if (rows.every((r) => r.nodes.length === 0)) {
    resetMap();
    const box = el("p", { class: "map-empty is-dead-end" },
      `No complete stack exists with ${labelFor(state.pin.product)} ${state.pin.version}` +
      (state.hideLegacy ? " among non-legacy releases." : "."));
    // The solver knows why. Without this the reader cannot tell a gap in the data from an
    // incompatibility from a layer they opened a moment ago.
    if (state.deadEnd?.reason) {
      box.append(el("span", { class: "dead-end-reason" }, state.deadEnd.reason));
    }
    if (state.deadEnd?.closeLayer) {
      const key = state.deadEnd.closeLayer;
      box.append(el("button", {
        class: "ghost inline",
        type: "button",
        onclick: () => toggleLayer(key),
      }, `Remove ${labelFor(key)}`));
    }
    host.append(box);
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
//
// The element is always the full width of its frame; what changes with the reader's
// selection is the world, and what changes with the reader's zoom is the viewBox. Width
// used to be pinned to the world size, which made the drawing permanently 1:1 and left
// `overflow-x: auto` as the only way to see a wide row.
function ensureSVG(host, width, height) {
  if (!mapDOM.svg) {
    host.textContent = "";
    mapDOM.svg = svgEl("svg", {
      class: "map-svg", role: "presentation",
      preserveAspectRatio: "xMidYMid meet",
    });
    mapDOM.links = svgEl("g", { class: "map-links" });     // under the nodes
    mapDOM.labels = svgEl("g", { class: "map-labels" });
    mapDOM.svg.append(mapDOM.links, mapDOM.labels);
    host.append(mapDOM.svg);
    bindMapViewport();
  }
  mapDOM.svg.setAttribute("width", "100%");

  world = { w: width, h: height };
  // Every selection re-fits, until the reader takes the view somewhere themselves —
  // snapping their zoom back on each click would make the map unusable at exactly the
  // moment they were reading it closely. Reset gives the automatic fit back.
  if (view.dirty) applyView();
  else fitView();
  return mapDOM.svg;
}

// bindMapViewport wires zoom and pan, once, to the drawing that outlives every render.
//
// A plain wheel is deliberately left alone. Hijacking it to zoom traps the page: a reader
// scrolling down to the version lists would instead scale the map and never get past it.
// ctrl/⌘+wheel is the browser's own zoom gesture and is what a trackpad pinch sends, so
// that is the one taken — with the buttons carrying discoverability for everyone else.
function bindMapViewport() {
  const svg = mapDOM.svg;

  svg.addEventListener("wheel", (ev) => {
    if (!ev.ctrlKey && !ev.metaKey) return; // let the page scroll
    ev.preventDefault();
    const at = pointToWorld(ev);
    if (at) zoomAbout(at.x, at.y, ev.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP);
  }, { passive: false });

  // A pan ends with a click the browser fires anyway. Swallowing it with a one-shot
  // listener leaks: a drag released outside the drawing may produce no click at all, and
  // the listener would then eat the next real one. A timestamp cannot leak.
  let swallowClickUntil = 0;
  svg.addEventListener("click", (ev) => {
    if (performance.now() > swallowClickUntil) return;
    swallowClickUntil = 0;
    ev.stopPropagation();
    ev.preventDefault();
  }, { capture: true });

  // Drag to pan, but only past a threshold: a click on a node has to stay a click, and a
  // few pixels of pointer travel during one is normal.
  let drag = null;
  svg.addEventListener("pointerdown", (ev) => {
    if (ev.button !== 0) return;
    const at = pointToWorld(ev);
    if (!at) return;
    drag = { from: at, origin: { x: view.x, y: view.y }, moved: false, id: ev.pointerId };
  });

  svg.addEventListener("pointermove", (ev) => {
    if (!drag || ev.pointerId !== drag.id) return;
    const at = pointToWorld(ev);
    if (!at) return;
    // Measured against the drag's own start point in world units, which already accounts
    // for the current scale.
    const dx = at.x - drag.from.x;
    const dy = at.y - drag.from.y;
    if (!drag.moved && Math.abs(dx) * view.k < 4 && Math.abs(dy) * view.k < 4) return;
    if (!drag.moved) {
      drag.moved = true;
      svg.setPointerCapture(drag.id);
      svg.classList.add("is-panning");
      hidePeek(true);
    }
    view.x = drag.origin.x - dx;
    view.y = drag.origin.y - dy;
    view.dirty = true;
    applyView();
  });

  const endDrag = (ev) => {
    if (!drag || ev.pointerId !== drag.id) return;
    if (drag.moved) {
      svg.releasePointerCapture(drag.id);
      svg.classList.remove("is-panning");
      // Arm the swallow above, so this pan does not also pin a version.
      swallowClickUntil = performance.now() + 300;
    }
    drag = null;
  };
  svg.addEventListener("pointerup", endDrag);
  svg.addEventListener("pointercancel", endDrag);
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
  g.onfocus = () => {
    // Tabbing has to bring the node with it. The viewBox is not a scroll offset, so the
    // browser's own scroll-into-view has nothing to move and a node off the edge of the
    // viewport would take focus while staying invisible.
    panIntoView({
      x: Number(g.dataset.x), y: Number(g.dataset.y),
      w: Number(g.querySelector(".map-node-box")?.getAttribute("width")) || 0,
      h: MAP.nodeH,
    });
    showPeek(g, layer, node);
    traceNode(node.id);
  };
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

    // A closed optional layer collapses to one row saying when it applies. Each has its
    // own toggle and its own state — opening NSX must never open Avi.
    if (!isOpen(layer)) {
      root.append(
        el("div", { class: "layer is-optional is-closed" },
          el("div", { class: "layer-spine" },
            el("div", { class: "layer-name" }, layer.label),
            el("div", { class: "layer-count" }, `${total}`)),
          el("div", { class: "layer-nodes" },
            el("button", {
              class: "ghost inline layer-toggle",
              type: "button",
              "aria-expanded": "false",
              onclick: () => toggleLayer(layer.key),
            }, `Add ${layer.label}`),
            layer.note ? el("span", { class: "layer-note" }, layer.note) : null)));
      continue;
    }

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

    if (layer.optional) {
      nodes.append(el("button", {
        class: "ghost inline layer-toggle",
        type: "button",
        "aria-expanded": "true",
        onclick: () => toggleLayer(layer.key),
      }, `Remove ${layer.label}`));
    }

    root.append(
      el("div", { class: layer.optional ? "layer is-optional" : "layer" },
        el("div", { class: "layer-spine" },
          el("div", { class: "layer-name" }, layer.label), count),
        nodes));
  }
}

// toggleLayer opens or closes one optional layer.
//
// Closing a layer drops any pin that lived in it — leaving a pin on a hidden layer would
// mean the map is solving for something the reader cannot see. Nothing else is touched:
// the other optional layer keeps whatever state it had.
function toggleLayer(key) {
  const wasOpen = state.include.includes(key);
  state.include = wasOpen
    ? state.include.filter((k) => k !== key)
    : [...state.include, key];

  if (wasOpen && state.pin?.product === key) {
    setPin(null);
    return;
  }
  syncURL(true);
  loadStack();
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
    // A line keeps its best phase as its headline, but a node holding worse builds has
    // to admit it, or picking one out of it is a coin toss. This applies to an amber
    // node as much as a green one: VKr 1.26 reads Technical Guidance off a single build
    // upstream mislabelled, while the other five went End of Support in January 2025.
    node.mixed && node.worstPhase !== node.phase
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
  // Forced: the map is about to be redrawn, so details for the old picture are stale.
  hidePeek(true);
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

const URL_DEFAULTS = { legacy: true, view: "stack" };

function currentURL() {
  const params = new URLSearchParams();
  if (state.pin) {
    params.set("product", state.pin.product);
    params.set("version", state.pin.version);
  }
  if (state.hideLegacy !== URL_DEFAULTS.legacy) params.set("legacy", "1");
  if (state.view !== URL_DEFAULTS.view) params.set("view", state.view);
  // Which optional layers are open travels with the link, so a URL shared as "here is
  // the Avi-only view" comes back as the Avi-only view.
  if (withParam()) params.set("with", withParam());
  // The generation travels with the link too: "here is the vSphere 9 view" has to come
  // back as the vSphere 9 view, not as every generation with a 9 pinned.
  if (genParam()) params.set("gen", genParam());
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
  return {
    pin: state.pin, hideLegacy: state.hideLegacy, view: state.view,
    include: state.include, generation: state.generation,
  };
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
  state.view = params.get("view") === "usage" ? "usage" : "stack";
  // Normalise rather than trust: the value is lowercased and filtered to keys that
  // actually name an optional layer, so `?with=NSX` opens NSX and `?with=haproxy` opens
  // nothing instead of half-applying.
  const asked = (params.get("with") || "")
    .split(",").map((k) => k.trim().toLowerCase()).filter(Boolean);
  state.include = OPTIONAL_ORDER.filter((k) => asked.includes(k));
  // Same treatment for the generation: a value the build does not offer widens to All
  // rather than erroring, so an old link still shows a map. state.generations is empty
  // until meta lands, so the first read trusts the number and renderGenerations corrects
  // it once the server's list is in.
  state.generation = readGeneration(params.get("gen"));
  // A `with` that resolved to nothing did not ask for anything. Counting it anyway
  // suppressed both the saved stack and the default vCenter pin, so `?with=` on its own
  // opened the app on a blank map.
  return params.has("product") || params.has("legacy") ||
         params.has("view") || state.include.length > 0 || state.generation !== 0;
}

// readGeneration parses the `gen` parameter, falling back to every generation.
function readGeneration(raw) {
  const gen = Number.parseInt(raw ?? "", 10);
  if (!Number.isInteger(gen) || gen <= 0) return 0;
  // Before meta lands there is no list to check against, so an unknown-but-plausible
  // number is allowed through and settled by renderGenerations.
  if (state.generations.length > 0 && !state.generations.includes(gen)) return 0;
  return gen;
}

window.addEventListener("popstate", () => {
  restoring = true;
  applyURL();
  showView(state.view);
  // Back across a generation change has to move the tabs and the manifest with it, not
  // only the map.
  renderGenerations();
  reloadMeta();
  loadStack().finally(() => { restoring = false; });
});

// --- the generation picker --------------------------------------------------
//
// A generation is a statement about vCenter alone. Under vSphere 9 the map still offers
// ESX 8.x, NSX 4.x, Avi 31.x and the Supervisor vsc0 train, because upstream publishes all
// of them against vCenter 9 — in NSX's case more compatible pairs than NSX 9.x manages.
// Filtering each layer by its own major would read tidier and be wrong.

// metaPath asks for the manifest belonging to the current generation. Release counts
// narrow with the filter, so a shared manifest would contradict the map beside it.
function metaPath() {
  return "/api/meta" + (genParam() ? `?gen=${genParam()}` : "");
}

// loadMeta fetches the manifest, widening to every generation if the one asked for has no
// bundle at all. On a static build that is a 404 for a file the site never wrote, which
// would otherwise leave the page with no manifest and no tabs to escape with.
async function loadMeta() {
  try {
    return await api(metaPath());
  } catch (err) {
    if (!state.generation) throw err;
    state.generation = 0;
    syncURL(false);
    return api(metaPath());
  }
}

async function reloadMeta() {
  try {
    const meta = await api(metaPath());
    state.meta = meta;
    renderMeta(meta);
  } catch (err) {
    $("#meta").textContent = err.message;
  }
}

function renderGenerations() {
  const host = $("#generations");
  if (!host) return;
  host.textContent = "";
  // Nothing to choose between until the server says what it offers.
  if (state.generations.length === 0) return;

  for (const gen of [0, ...state.generations]) {
    const active = gen === state.generation;
    host.append(el("button", {
      type: "button",
      class: "segment" + (active ? " is-active" : ""),
      "aria-pressed": String(active),
      title: gen
        ? `Only stacks built on vCenter ${gen}.x. Older ESX, NSX and Avi releases that ` +
          `pair with it are still offered.`
        : "Every generation.",
      onclick: () => setGeneration(gen),
    }, generationLabel(gen)));
  }
}

// setGeneration switches generation and re-asks the server.
//
// The pin is kept when it survives the switch and dropped when it does not. A vCenter 8
// pin cannot exist under vSphere 9 at all, and a line on any other layer can disappear
// too once it can no longer reach a vCenter in range — so rather than predict which,
// this tries the pin and falls back to the unpinned map for that generation.
async function setGeneration(gen) {
  if (gen === state.generation) return;
  state.generation = gen;
  renderGenerations();
  syncURL(true); // a generation change is a real step, so back returns to the last one

  const metaDone = reloadMeta();
  if (!(await loadStack()) && state.pin) {
    state.pin = null;
    syncURL(false);
    await loadStack();
  }
  await metaDone;
}

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
  // Bottom-up, matching the map above it. NSX and Avi only appear when the reader has
  // opted into them, which is exactly when the solver put one in the stack.
  for (const key of ["vcenter", "esx", "nsx", "avi", "supervisor", "vks", "vkr"]) {
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
// the Supervisor it manages, so it moves first; the guest clusters move last. NSX and Avi
// sit in between — both are moved before the Supervisor that runs on them — and drop out
// of the manifest entirely when the stack does not include them.
const UPGRADE_ORDER = ["vcenter", "esx", "nsx", "avi", "supervisor", "vks", "vkr"];

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
      eogs: pick.eogs || "",
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
  nsx: "optional — the networking the Supervisor runs on, moved before it",
  avi: "optional — the load balancer in front of the Supervisor, moved before it",
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
        (s.legacy ? `  ⚠ ${s.phaseLabel}${s.eogs ? ` since ${s.eogs}` : ""}` : "") +
        (s.pinned ? "  _(the version you pinned)_" : ""));
    });
    lines.push("", `Do not reorder. ${EDGE_PROSE.vcenter[0].toUpperCase()}` +
      EDGE_PROSE.vcenter.slice(1) + ".", "");
  } else {
    lines.push(`# Target stack`, "");
    lines.push(`Selection: ${m.selection}`, "");
    lines.push("| Order | Component | Version | Support phase | General support ends | Why here |");
    lines.push("|---|---|---|---|---|---|");
    m.steps.forEach((s, i) => {
      lines.push(`| ${i + 1} | ${s.label} | \`${s.version}\` | ${s.phaseLabel} | ` +
        `${s.eogs || "not published"} | ${s.why} |`);
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
  const known = {
    vcenter: "vCenter", esx: "ESX", nsx: "NSX", avi: "Avi",
    supervisor: "Supervisor", vks: "VKS", vkr: "VKr",
  };
  return known[key] || key;
}

// --- peek popover ----------------------------------------------------------

// peekLock holds the popover open when the reader asked for it, rather than only while
// the pointer is perfectly still. The popover now carries the exact release list,
// provenance and upstream footnotes — content worth reading and copying, which a
// hover-only, pointer-events:none tooltip makes impossible.
let peekLock = null;

// The rail takes over from the floating popover whenever the viewport is wide enough to
// give it a column of its own. Below that there is nowhere to put it that is not on top
// of the map, so the popover stays.
const railQuery = window.matchMedia("(min-width: 62rem)");
const railActive = () => railQuery.matches;
railQuery.addEventListener("change", () => {
  hidePeek(true);
  renderRailPlaceholder();
});

// renderRailPlaceholder says what the rail is for while nothing is selected, rather than
// leaving an unexplained empty column beside the map.
function renderRailPlaceholder() {
  const rail = $("#peek-rail");
  if (!rail) return;
  rail.textContent = "";
  if (!railActive()) return;
  rail.append(el("div", { class: "peek-rail-empty" },
    "Hover a node for its exact builds, provenance and upstream notes."));
}

// peekParts builds what a node has to say, independent of where it will be shown.
// Returns null when there is nothing worth showing. How much of it survives is the
// caller's decision: the rail has a column to itself, a floating popover does not.
function peekParts(layer, node) {
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
  // Why this node hands over an older build than its newest. Placed with the other
  // head lines so it is read before the release list, not after it.
  if (node.pinNote) head.push(node.pinNote);
  if (node.legacy && node.phaseSource === "lifecycle") {
    // Do not blame the interop checkbox for this one: the matrix still lists these as
    // generally supported. Broadcom's lifecycle matrix is what retired them.
    head.push(`${node.phaseLabel}${node.eogs ? ` — general support ended ${node.eogs}` : ""}. ` +
      `Broadcom's product lifecycle matrix retired this; the interoperability matrix ` +
      `still lists it as supported.`);
  } else if (node.legacy) {
    head.push(`${node.phaseLabel} — this is what the interop site's ` +
      `"hide legacy releases" checkbox removes.`);
  }
  if (node.mixed && node.worstPhase !== node.phase) {
    head.push(`Not every build in this line: the oldest is ${node.worstPhaseLabel}.`);
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
  if (!head.length && !body.length) return null;
  return { title: `${layer.label} ${node.label}`, head, body };
}

function showPeek(anchor, layer, node) {
  // The rail ignores the lock. Locking exists so a floating popover survives the pointer
  // leaving it; the rail already stays put, and honouring a lock there would only freeze
  // it on one node while the reader hovers others.
  if (railActive()) { showPeekInRail(layer, node); return; }
  if (peekLock && peekLock !== node.id) return; // a locked popover is not overwritten

  const peek = $("#peek");
  peek.textContent = "";
  const parts = peekParts(layer, node);
  if (!parts) return;
  const { head, body } = parts;

  const locked = peekLock === node.id;
  // Locked, the whole list is shown and stays put; hovering, it is capped so the popover
  // does not swallow the page.
  const room = locked ? body.length : Math.max(0, 16 - head.length);
  peek.append(el("div", { class: "peek-title" },
    parts.title,
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

// showPeekInRail renders the same content into the column beside the map.
//
// Nothing is truncated and the copy button is always there, because the rail is not
// competing with the drawing for space. It also does not need a lock to be readable: the
// content stays put when the pointer leaves, so it can be read and selected without
// holding a hover steady.
function showPeekInRail(layer, node) {
  const rail = $("#peek-rail");
  if (!rail) return;
  const parts = peekParts(layer, node);
  if (!parts) return;
  rail.textContent = "";
  rail.append(el("div", { class: "peek-title" }, parts.title));
  for (const line of parts.head) rail.append(el("div", { class: "peek-head" }, line));
  for (const line of parts.body) rail.append(el("div", {}, line));
  if (parts.body.length) {
    rail.append(el("div", { class: "peek-actions" },
      el("button", {
        class: "ghost", type: "button",
        onclick: (ev) => copyText([...parts.body].join("\n"), ev.currentTarget, "Copied"),
      }, "Copy releases")));
  }
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
  if (railActive()) { showPeekInRail(layer, node); return; } // nothing to keep open
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
  // The rail keeps the last node's details on an ordinary mouseleave. Clearing them the
  // instant the pointer moves off would make anything longer than a glance unreadable,
  // which is the failure the rail exists to fix. An explicit dismissal still clears it.
  if (force) renderRailPlaceholder();
}

// --- tabs -------------------------------------------------------------------

function showView(name) {
  state.view = name;
  for (const v of ["stack", "usage"]) {
    $(`#view-${v}`).classList.toggle("hidden", v !== name);
  }
  for (const btn of document.querySelectorAll("#tabs button")) {
    btn.classList.toggle("active", btn.dataset.view === name);
  }
  // The tab is a personal habit rather than part of the view being pointed at, so it is
  // remembered here and only reaches a link when it is not the default.
  localStorage.setItem("vkstack.view", name);

  // A hidden element has no width, so a map laid out while the usage tab was showing was
  // fitted against a frame of zero. Re-fit on the way back, now that there is a frame.
  if (name === "stack" && mapDOM.svg && world.w) {
    if (view.dirty) applyView();
    else fitView();
  }
}

for (const btn of document.querySelectorAll("#tabs button")) {
  btn.addEventListener("click", () => {
    showView(btn.dataset.view);
    syncURL(false);
  });
}

// The usage snippets are meant to be pasted into a config file, so they get a button
// rather than a selection. Same clipboard path as "Copy link", fallback included: the
// Clipboard API is unavailable on a shared instance served over plain http.
for (const btn of document.querySelectorAll("[data-copy]")) {
  btn.addEventListener("click", () => {
    const source = document.getElementById(btn.dataset.copy);
    if (source) copyText(source.textContent, btn, "Copied");
  });
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
  renderRailPlaceholder();

  // The URL is a more specific instruction than any default, so it is read first. Then
  // this browser's own stack, if one was saved. Only failing both does the map fall back
  // to the newest base version, so an arrival is never an empty frame.
  //
  // It is also read before the manifest, because it names the generation and the manifest
  // answers for one: per-product release counts narrow with the filter.
  const fromURL = applyURL();

  try {
    // Kept, not just printed: the manifest has to stamp which snapshot it came from.
    state.meta = await loadMeta();
    state.generations = Array.isArray(state.meta.generations) ? state.meta.generations : [];
    // Settle a generation the link asked for that this build does not offer. applyURL let
    // it through because the list had not arrived yet, and a served instance answers such
    // a request by widening rather than by failing.
    if (state.generation && !state.generations.includes(state.generation)) {
      state.generation = 0;
      state.meta = await api(metaPath());
      syncURL(false);
    }
    renderGenerations();
    renderMeta(state.meta);
  } catch (err) {
    $("#meta").textContent = err.message;
  }

  showView(fromURL ? state.view : (localStorage.getItem("vkstack.view") || "stack"));
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

$("#map-zoom-in").addEventListener("click", () => zoomBy(ZOOM_STEP));
$("#map-zoom-out").addEventListener("click", () => zoomBy(1 / ZOOM_STEP));
$("#map-fit").addEventListener("click", () => { view.dirty = true; fitView(); });
// Reset clears the flag as well as the numbers, so the map goes back to fitting itself
// on every selection rather than holding whatever the reader last set.
$("#map-reset").addEventListener("click", resetView);

// Panning and zooming from the keyboard, for anyone not using a pointer. The handler is
// on the frame rather than the SVG because the SVG is replaced whenever the map empties.
$("#map").addEventListener("keydown", (ev) => {
  if (ev.target !== ev.currentTarget) return; // a node has focus; leave its keys alone
  if (!mapDOM.svg || !world.w) return;
  const step = 60 / view.k;
  switch (ev.key) {
    case "ArrowLeft":  view.x -= step; break;
    case "ArrowRight": view.x += step; break;
    case "ArrowUp":    view.y -= step; break;
    case "ArrowDown":  view.y += step; break;
    case "+": case "=": zoomBy(ZOOM_STEP); ev.preventDefault(); return;
    case "-": case "_": zoomBy(1 / ZOOM_STEP); ev.preventDefault(); return;
    case "0": resetView(); ev.preventDefault(); return;
    default: return;
  }
  ev.preventDefault();
  view.dirty = true;
  applyView();
});

// A narrower window means a different fit. Only the untouched view follows it: recomputing
// over a zoom the reader set would undo their work on every resize.
window.addEventListener("resize", () => {
  if (!mapDOM.svg || !world.w) return;
  if (view.dirty) applyView();
  else fitView();
});

// Printing wants the whole drawing on the page, not the window onto it.
if (window.matchMedia) {
  const printing = window.matchMedia("print");
  const showAll = (on) => {
    if (!mapDOM.svg || !world.w) return;
    if (on) mapDOM.svg.setAttribute("viewBox", `0 0 ${world.w} ${world.h}`);
    else applyView();
  };
  printing.addEventListener?.("change", (ev) => showAll(ev.matches));
  window.addEventListener("beforeprint", () => showAll(true));
  window.addEventListener("afterprint", () => showAll(false));
}
document.addEventListener("keydown", (ev) => {
  if (ev.key === "Escape") {
    const railShowing = railActive() && $("#peek-rail")?.querySelector(".peek-title");
    if (railShowing || !$("#peek").classList.contains("hidden")) { hidePeek(true); return; }
    if (state.pin) setPin(null);
  }
});
window.addEventListener("scroll", () => hidePeek(), { passive: true });

boot();
