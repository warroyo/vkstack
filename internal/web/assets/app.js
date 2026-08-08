"use strict";

// Everything is served from the same origin and the whole dataset is small, so the app
// re-asks the server on each change rather than mirroring the graph in the browser.

const state = {
  products: [],
  pins: {},        // stack builder: product key -> version
  planFrom: {},
  planTo: {},
};

const $ = (sel) => document.querySelector(sel);
const el = (tag, attrs = {}, ...kids) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined) node.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined) continue;
    node.append(kid.nodeType ? kid : document.createTextNode(kid));
  }
  return node;
};

async function api(path, options) {
  const res = await fetch(path, options);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `request failed (${res.status})`);
  return body;
}

const postJSON = (path, payload) =>
  api(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });

// --- mermaid ---------------------------------------------------------------

const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
mermaid.initialize({
  startOnLoad: false,
  securityLevel: "strict",
  theme: prefersDark ? "dark" : "default",
  flowchart: { curve: "basis", nodeSpacing: 45, rankSpacing: 55, htmlLabels: true },
});

let diagramSeq = 0;
async function renderDiagram(target, definition) {
  target.textContent = "";
  try {
    const { svg } = await mermaid.render(`d${++diagramSeq}`, definition);
    target.innerHTML = svg;
  } catch (err) {
    // A diagram that will not parse should say so rather than leaving a blank panel.
    target.append(el("div", { class: "error" }, `could not render diagram: ${err.message}`));
  }
}

// --- tabs ------------------------------------------------------------------

const VIEWS = ["model", "stack", "graph", "plan"];

function showView(name) {
  for (const v of VIEWS) {
    $(`#view-${v}`).classList.toggle("hidden", v !== name);
  }
  for (const btn of document.querySelectorAll("#tabs button")) {
    btn.classList.toggle("active", btn.dataset.view === name);
  }
  // Remember the tab so returning users land on the tool, while newcomers still get
  // the explainer first.
  localStorage.setItem("interop.view", name);
  if (name === "graph") refreshGraphView();
}

// --- model view ------------------------------------------------------------

async function loadModel() {
  const data = await api("/api/model");
  await renderDiagram($("#model-diagram"), data.mermaid);

  const list = $("#model-prose");
  list.textContent = "";
  for (const e of data.edges) {
    list.append(
      el("li", {},
        el("strong", {}, `${e.from} → ${e.to}`),
        ` — ${e.prose}`,
        e.published ? null : el("span", { class: "unpublished" }, " (not published upstream)")
      )
    );
  }
}

// --- shared pickers --------------------------------------------------------

async function versionsFor(key) {
  const rels = await api(`/api/releases?product=${encodeURIComponent(key)}`);
  return rels.map((r) => r.version);
}

// buildPickers renders one select per product. `options` optionally restricts each
// product's choices, which is what makes the stack builder narrow as you pin things.
function buildPickers(container, selected, onChange, options = null) {
  container.textContent = "";
  for (const p of state.products) {
    const allowed = options && options[p.key] ? options[p.key] : p.versions;
    const current = selected[p.key] || "";
    const select = el("select", {
      class: current ? "pinned" : "",
      onchange: (ev) => onChange(p.key, ev.target.value),
    });
    select.append(el("option", { value: "" }, `any (${allowed.length})`));
    for (const v of allowed) {
      select.append(el("option", { value: v, ...(v === current ? { selected: "selected" } : {}) }, v));
    }
    // A pinned version that the current narrowing excludes must still be visible,
    // otherwise the control would silently drop the user's own choice.
    if (current && !allowed.includes(current)) {
      select.append(el("option", { value: current, selected: "selected" }, `${current} (conflicts)`));
    }
    container.append(el("div", { class: "picker" }, el("label", {}, p.label), select));
  }
}

// --- stack builder ---------------------------------------------------------

async function refreshStack() {
  const payload = { pins: state.pins, patches: $("#stack-patches").checked };
  let data;
  try {
    data = await postJSON("/api/stack", payload);
  } catch (err) {
    $("#stack-error").textContent = err.message;
    $("#stack-error").classList.remove("hidden");
    return;
  }

  const errBox = $("#stack-error");
  const out = $("#stack-result");
  out.textContent = "";

  if (!data.ok) {
    errBox.textContent = data.error;
    errBox.classList.remove("hidden");
    buildPickers($("#stack-pickers"), state.pins, onStackPin);
    return;
  }
  errBox.classList.add("hidden");
  buildPickers($("#stack-pickers"), state.pins, onStackPin, data.options);

  const dl = el("dl");
  for (const p of state.products) {
    const version = data.recommended[p.key];
    if (!version) continue;
    dl.append(
      el("dt", {}, p.label),
      el("dd", { class: "mono" }, version,
        state.pins[p.key] ? el("span", { class: "pin-badge" }, "pinned") : null)
    );
  }
  out.append(el("div", { class: "stack-summary" },
    el("h2", {}, "Recommended stack"), dl));

  out.append(el("h2", {}, "Pair by pair"));
  out.append(verdictTable(data.verdicts));

  if (data.inferred && data.inferred.length) {
    out.append(el("div", { class: "note" },
      `Inferred rather than verified — upstream publishes no data for: ${data.inferred.join(", ")}.`));
  }
}

function verdictTable(verdicts) {
  const rows = verdicts.map((v) =>
    el("tr", {},
      el("td", {}, `${v.a} × ${v.b}`),
      el("td", { class: "mono" }, `${v.aVersion} / ${v.bVersion}`),
      el("td", { class: `state-${v.state}` }, v.detail))
  );
  return el("div", { class: "table-wrap" },
    el("table", {},
      el("thead", {}, el("tr", {},
        el("th", {}, "Pair"), el("th", {}, "Versions"), el("th", {}, "Verdict"))),
      el("tbody", {}, rows)));
}

function onStackPin(key, value) {
  if (value) state.pins[key] = value;
  else delete state.pins[key];
  refreshStack();
}

// --- graph view ------------------------------------------------------------

let graphFocus = { product: null, version: null };

function buildGraphPickers() {
  const container = $("#graph-pickers");
  container.textContent = "";

  const productSelect = el("select", {
    onchange: (ev) => {
      graphFocus = { product: ev.target.value, version: null };
      buildGraphPickers();
      refreshGraphView();
    },
  });
  for (const p of state.products) {
    productSelect.append(el("option", {
      value: p.key, ...(p.key === graphFocus.product ? { selected: "selected" } : {}),
    }, p.label));
  }

  const product = state.products.find((p) => p.key === graphFocus.product) || state.products[0];
  graphFocus.product = product.key;
  if (!graphFocus.version) graphFocus.version = product.versions[0];

  const versionSelect = el("select", {
    onchange: (ev) => { graphFocus.version = ev.target.value; refreshGraphView(); },
  });
  for (const v of product.versions) {
    versionSelect.append(el("option", {
      value: v, ...(v === graphFocus.version ? { selected: "selected" } : {}),
    }, v));
  }

  container.append(
    el("div", { class: "picker" }, el("label", {}, "Focus on"), productSelect),
    el("div", { class: "picker" }, el("label", {}, "Version"), versionSelect));
}

async function refreshGraphView() {
  if (!graphFocus.product || !graphFocus.version) return;
  const url = `/api/graph?product=${encodeURIComponent(graphFocus.product)}` +
    `&version=${encodeURIComponent(graphFocus.version)}`;
  try {
    const data = await api(url);
    await renderDiagram($("#graph-diagram"), data.mermaid);
    const note = $("#graph-note");
    note.textContent = data.note || "";
    note.classList.toggle("hidden", !data.note);
  } catch (err) {
    $("#graph-diagram").textContent = "";
    $("#graph-diagram").append(el("div", { class: "error" }, err.message));
  }
}

// --- upgrade planner -------------------------------------------------------

async function runPlan() {
  const out = $("#plan-result");
  out.textContent = "";
  let data;
  try {
    data = await postJSON("/api/plan", { from: state.planFrom, to: state.planTo });
  } catch (err) {
    out.append(el("div", { class: "error" }, err.message));
    return;
  }
  if (!data.ok) {
    out.append(el("div", { class: "error" }, data.error));
    if (data.reached && Object.keys(data.reached).length) {
      const dl = el("dl");
      for (const p of state.products) {
        if (data.reached[p.key]) dl.append(el("dt", {}, p.label), el("dd", { class: "mono" }, data.reached[p.key]));
      }
      out.append(el("div", { class: "stack-summary" }, el("h2", {}, "Furthest reachable stack"), dl));
    }
    return;
  }

  out.append(el("h2", {}, `${data.steps} step(s) in ${data.windows.length} maintenance window(s)`));
  let n = 0;
  data.windows.forEach((win, i) => {
    const steps = el("ol");
    for (const s of win.steps) {
      n++;
      steps.append(el("li", {},
        el("strong", {}, `Upgrade ${s.product}`),
        el("br"),
        el("span", { class: "mono" }, `${s.from} → ${s.to}`),
        s.supported ? null : el("span", { class: "blocking" }, `transitional: ${s.blocking}`)));
    }
    out.append(el("div", { class: `window${win.transitional ? " transitional" : ""}` },
      el("h3", {}, `Window ${i + 1}`),
      win.transitional
        ? el("div", { class: "warn" }, "Do not stop partway — the stack is unsupported until the last step.")
        : null,
      steps));
  });
}

// --- boot ------------------------------------------------------------------

async function boot() {
  let meta;
  try {
    meta = await api("/api/meta");
  } catch (err) {
    $("#meta").append(el("span", { class: "state-bad" },
      `${err.message} — run \`interop refresh\` and reload.`));
    // The whiteboard needs no data, so still offer it.
    await loadModel().catch(() => {});
    return;
  }

  $("#meta").textContent =
    `data fetched ${meta.fetchedAt} (${meta.ageHours}h ago) · ` +
    meta.products.map((p) => `${p.label} ${p.count}`).join(" · ");

  state.products = meta.products;
  await Promise.all(state.products.map(async (p) => { p.versions = await versionsFor(p.key); }));

  await loadModel();
  await refreshStack();
  buildGraphPickers();

  buildPickers($("#plan-from"), state.planFrom, (k, v) => {
    if (v) state.planFrom[k] = v; else delete state.planFrom[k];
  });
  buildPickers($("#plan-to"), state.planTo, (k, v) => {
    if (v) state.planTo[k] = v; else delete state.planTo[k];
  });

  showView(localStorage.getItem("interop.view") || "model");
}

for (const btn of document.querySelectorAll("#tabs button")) {
  btn.addEventListener("click", () => showView(btn.dataset.view));
}
$("#to-stack").addEventListener("click", () => showView("stack"));
$("#stack-reset").addEventListener("click", () => { state.pins = {}; refreshStack(); });
$("#stack-patches").addEventListener("change", refreshStack);
$("#plan-run").addEventListener("click", runPlan);

boot();
