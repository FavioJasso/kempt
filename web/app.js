// kempt web demo. Vanilla JS, no build step. Every value from a spreadsheet
// is inserted with textContent, never innerHTML.
"use strict";

const $ = (sel, el = document) => el.querySelector(sel);
const $$ = (sel, el = document) => Array.from(el.querySelectorAll(sel));

const ROLES = [
  ["title", "Title"],
  ["header", "Header"],
  ["data", "Data"],
  ["total", "Total (dropped)"],
  ["total_kept", "Total (kept)"],
  ["footer", "Footer"],
  ["null", "NULL"],
  ["ignored", "Ignored"],
];

const state = {
  config: { loadEnabled: false, demo: false },
  source: null, // {file: File} or {sample: "name"}
  result: null,
  sheet: 0,
  table: 0,
  dialect: "postgres",
  tab: "clean",
  load: null, // {schema, token, table, tables, queryEnabled, expiresAt}
  cells: null, // Map "r,c" → td (merge-covered cells map to their anchor)
  active: null,
};

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children) if (c != null) node.append(c);
  return node;
}

// ---- A1 helpers ---------------------------------------------------------

function colIndex(letters) {
  let n = 0;
  for (const ch of letters.toUpperCase()) n = n * 26 + (ch.charCodeAt(0) - 64);
  return n - 1;
}

function colName(c) {
  let s = "";
  for (c += 1; c > 0; c = Math.floor((c - 1) / 26)) s = String.fromCharCode(65 + ((c - 1) % 26)) + s;
  return s;
}

function parseRef(ref) {
  const m = /^([A-Za-z]+)(\d+)$/.exec(ref);
  return m ? { r: Number(m[2]) - 1, c: colIndex(m[1]) } : null;
}

function parseRange(range) {
  const bare = range.includes("!") ? range.slice(range.lastIndexOf("!") + 1) : range;
  const [a, b] = bare.split(":");
  const p = parseRef(a), q = parseRef(b || a);
  if (!p || !q) return null;
  return { r1: Math.min(p.r, q.r), c1: Math.min(p.c, q.c), r2: Math.max(p.r, q.r), c2: Math.max(p.c, q.c) };
}

// ---- API ----------------------------------------------------------------

function formData(source) {
  const fd = new FormData();
  if (source.file) fd.append("file", source.file, source.file.name);
  else fd.append("sample", source.sample);
  if ($("#opt-keep-totals").checked) fd.append("keep_totals", "1");
  const fields = { date_order: "#opt-date-order", lenient: "#opt-lenient", header_rows: "#opt-header-rows", skip_rows: "#opt-skip-rows", fill_down: "#opt-fill-down" };
  for (const [name, sel] of Object.entries(fields)) {
    const v = $(sel).value.trim();
    if (v) fd.append(name, v);
  }
  return fd;
}

async function api(path, init) {
  const res = await fetch(path, init);
  let body = null;
  try { body = await res.json(); } catch { /* not JSON */ }
  if (!res.ok) throw new Error((body && body.error) || `${res.status} ${res.statusText}`);
  return body;
}

function banner(msg, kind = "error") {
  const b = $("#banner");
  if (!msg) { b.hidden = true; return; }
  b.textContent = msg;
  b.className = "banner" + (kind === "info" ? " info" : "");
  b.hidden = false;
}

const OPTIONS = ["#opt-keep-totals", "#opt-date-order", "#opt-lenient", "#opt-header-rows", "#opt-skip-rows", "#opt-fill-down"];

// busy counts requests in flight, so one finishing does not re-enable the
// buttons while another (a load during an inspect) still runs. Options stay
// enabled, keeping keyboard focus: only the latest inspect renders anyway.
let busyCount = 0;

function busy(on) {
  busyCount = Math.max(0, busyCount + (on ? 1 : -1));
  const b = busyCount > 0;
  document.body.classList.toggle("busy", b);
  for (const el of $$(".chip, #load-btn, #run-sql")) el.disabled = b;
}

// Only the latest inspect request may render: a slow earlier response must
// not overwrite the result for options changed since. A load started before
// a newer inspect is dropped the same way.
let inspectSeq = 0;

async function inspect(source) {
  const seq = ++inspectSeq;
  banner(null);
  busy(true);
  try {
    const result = await api("/api/inspect", { method: "POST", body: formData(source) });
    if (seq !== inspectSeq) return;
    // The workspace, Load and option changes follow the file on screen, so
    // a failed upload leaves the previous file in charge.
    state.source = source;
    state.result = result;
    state.sheet = 0;
    state.table = 0;
    state.load = null;
    for (const c of $$(".chip")) c.setAttribute("aria-pressed", String(!!source.sample && c.dataset.sample === source.sample));
    render();
    if (!result.sheets.length) banner("The file has no sheets kempt can read.", "info");
  } catch (err) {
    if (seq !== inspectSeq) return;
    banner(err.message);
  } finally {
    busy(false);
  }
}

function showWarnings() {
  const sh = currentSheet();
  banner(sh && sh.warnings && sh.warnings.length ? sh.warnings.join("; ") : null, "info");
}

let loading = false;

async function load() {
  if (!state.source) return;
  const seq = inspectSeq;
  loading = true;
  busy(true);
  renderLoadState();
  try {
    const loaded = await api("/api/load", { method: "POST", body: formData(state.source) });
    if (seq !== inspectSeq) return; // another file or options are on screen now
    state.load = loaded;
    const t = currentTable();
    $("#sql").value = `SELECT *\nFROM ${quoteIdent(t ? t.table : state.load.table)}\nLIMIT 20;`;
    showWarnings();
    renderLoadState();
    selectTab("query");
  } catch (err) {
    if (seq === inspectSeq) banner(err.message);
  } finally {
    loading = false;
    busy(false);
    renderLoadState();
  }
}

async function runQuery() {
  if (!state.load) return;
  const out = $("#results");
  const meta = $("#query-meta");
  out.replaceChildren();
  meta.textContent = "Running…";
  busy(true);
  try {
    const res = await api("/api/query", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ schema: state.load.schema, token: state.load.token, sql: $("#sql").value }),
    });
    out.append(resultTable(res.columns, res.rows));
    meta.textContent = `${res.rowCount} row${res.rowCount === 1 ? "" : "s"}${res.truncated ? " (first 100 shown)" : ""} · ${res.elapsedMs} ms`;
  } catch (err) {
    meta.textContent = "";
    out.append(el("div", { class: "error", text: err.message }));
  } finally {
    busy(false);
  }
}

function quoteIdent(name) {
  return /^[a-z_][a-z0-9_]*$/.test(name) ? name : `"${name.replaceAll('"', '""')}"`;
}

// ---- Rendering ----------------------------------------------------------

function currentSheet() {
  return state.result ? state.result.sheets[state.sheet] : null;
}

function currentTable() {
  const sh = currentSheet();
  return sh && sh.tables.length ? sh.tables[Math.min(state.table, sh.tables.length - 1)] : null;
}

function render() {
  const r = state.result;
  $("#workspace").hidden = !r;
  if (!r) return;
  $("#file-name").textContent = r.file;
  const tabs = $("#sheet-tabs");
  tabs.replaceChildren();
  if (r.sheets.length > 1) {
    r.sheets.forEach((sh, i) => {
      tabs.append(el("button", {
        class: "chip", role: "tab", "aria-selected": String(i === state.sheet), "aria-pressed": String(i === state.sheet),
        text: sh.name, onclick: () => { state.sheet = i; state.table = 0; render(); },
      }));
    });
  }
  renderSheet();
  renderLoadState();
}

function renderSheet() {
  const sh = currentSheet();
  if (!sh) return;
  const g = sh.grid;
  $("#dims").textContent = g.totalRows > g.rows || g.totalCols > g.cols
    ? `${sh.dimensions} · showing ${g.rows}×${g.cols} of ${g.totalRows}×${g.totalCols}`
    : sh.dimensions || "empty sheet";
  renderLegend(sh);
  renderGrid(sh);
  renderTablePicker(sh);
  renderRight();
  showWarnings();
}

function renderLegend(sh) {
  const present = new Set(Object.values(sh.cellRoles));
  const ul = $("#legend");
  ul.replaceChildren();
  for (const [role, label] of ROLES) {
    if (!present.has(role) && !["header", "data"].includes(role)) continue;
    ul.append(el("li", {}, el("span", { class: `swatch r-${role}` }), label));
  }
}

function renderGrid(sh) {
  const g = sh.grid;
  const wrap = $("#grid-wrap");
  state.cells = new Map();
  state.active = null;
  if (!g.rows || !g.cols) {
    wrap.replaceChildren(el("p", { class: "hint", text: "This sheet is empty." }));
    return;
  }
  const anchors = new Map();
  const covered = new Map();
  for (const m of g.merges) {
    const rg = parseRange(m);
    if (!rg) continue;
    anchors.set(`${rg.r1},${rg.c1}`, rg);
    for (let r = rg.r1; r <= rg.r2; r++)
      for (let c = rg.c1; c <= rg.c2; c++)
        if (r !== rg.r1 || c !== rg.c1) covered.set(`${r},${c}`, `${rg.r1},${rg.c1}`);
  }

  const table = el("table", { class: "sheet" });
  const head = el("tr", {}, el("th", { class: "corner", text: "" }));
  for (let c = 0; c < g.cols; c++) head.append(el("th", { text: colName(c) }));
  table.append(el("thead", {}, head));
  const body = el("tbody");
  for (let r = 0; r < g.rows; r++) {
    const tr = el("tr", {}, el("th", { text: String(r + 1) }));
    for (let c = 0; c < g.cols; c++) {
      const key = `${r},${c}`;
      if (covered.has(key)) continue;
      const ref = colName(c) + (r + 1);
      const role = sh.cellRoles[ref];
      const kind = g.kinds[r][c];
      const td = el("td", { title: role ? `${ref} · ${role}` : ref, text: g.cells[r][c] });
      const classes = [];
      if (role) classes.push(`r-${role}`);
      if (kind === "n" || kind === "d") classes.push("num");
      td.className = classes.join(" ");
      const m = anchors.get(key);
      if (m) {
        if (m.r2 > m.r1) td.rowSpan = Math.min(m.r2, g.rows - 1) - m.r1 + 1;
        if (m.c2 > m.c1) td.colSpan = Math.min(m.c2, g.cols - 1) - m.c1 + 1;
      }
      state.cells.set(key, td);
      tr.append(td);
    }
    body.append(tr);
  }
  for (const [key, anchor] of covered) state.cells.set(key, state.cells.get(anchor));
  table.append(body);
  wrap.replaceChildren(table);
}

function renderTablePicker(sh) {
  const picker = $("#table-picker");
  picker.replaceChildren();
  picker.hidden = sh.tables.length < 2;
  sh.tables.forEach((t, i) => {
    picker.append(el("button", {
      class: "chip", "aria-pressed": String(i === state.table), text: `${t.table} (${t.region})`,
      onclick: () => { state.table = i; renderTablePicker(sh); renderRight(); },
    }));
  });
}

function renderRight() {
  const sh = currentSheet();
  const t = currentTable();
  $("#decision-count").textContent = sh ? `(${sh.decisions.length})` : "";
  renderClean(t);
  renderDDL(t);
  renderDecisions(sh);
  selectTab(state.tab);
}

function renderClean(t) {
  const box = $("#tab-clean");
  box.replaceChildren();
  if (!t) {
    box.append(el("p", { class: "hint", text: "No table was detected on this sheet. The Decisions tab explains why." }));
    return;
  }
  const table = el("table", { class: "clean" });
  const hr = el("tr");
  const numeric = t.columns.map((c) => /^(int|bigint|decimal)/.test(c.type));
  for (const c of t.columns) {
    hr.append(el("th", { title: c.header ? `Original header: ${c.header}` : "" },
      el("span", { class: "col-name", text: c.name }),
      el("span", { class: "col-type", text: c.type }),
      c.unit ? el("span", { class: "col-unit", text: `unit: ${c.unit}` }) : null));
  }
  table.append(el("thead", {}, hr));
  const body = el("tbody");
  for (const row of t.preview) {
    const tr = el("tr");
    row.forEach((v, j) => {
      tr.append(v === null
        ? el("td", { class: "nullv", text: "NULL" })
        : el("td", { class: numeric[j] ? "num" : "", text: v }));
    });
    body.append(tr);
  }
  table.append(body);
  box.append(el("div", { class: "clean-wrap" }, table));
  const shown = t.preview.length;
  box.append(el("p", { class: "meta", text: `${t.table}: ${t.rowCount} row${t.rowCount === 1 ? "" : "s"} × ${t.columns.length} columns from ${t.region}${shown < t.rowCount ? ` · first ${shown} shown` : ""}` }));
}

function renderDDL(t) {
  $("#ddl").textContent = t ? t.ddl[state.dialect] : "-- no table detected";
  for (const b of $$("#dialects button")) b.setAttribute("aria-pressed", String(b.dataset.dialect === state.dialect));
}

function renderDecisions(sh) {
  const list = $("#decisions");
  list.replaceChildren();
  if (!sh) return;
  for (const d of sh.decisions) {
    const pct = Math.round(d.confidence * 100);
    const btn = el("button", { class: "decision" + (d.confidence < 0.6 ? " low" : ""), type: "button" },
      el("span", { class: "kind", text: d.kind }),
      el("span", { class: "range", text: d.range }),
      el("span", { class: "conf", text: `${pct}%`, title: "confidence" }),
      el("span", { class: "reason", text: d.reason }),
      d.table && sh.tables.length > 1 ? el("span", { class: "table-tag", text: d.table }) : null,
      el("span", { class: "bar" }, el("span")));
    btn.lastChild.firstChild.style.width = `${pct}%`;
    btn.addEventListener("click", () => {
      for (const x of $$(".decision.active")) x.classList.remove("active");
      btn.classList.add("active");
      highlight(d.range);
    });
    list.append(el("li", {}, btn));
  }
}

function highlight(range) {
  for (const td of $$("td.hl")) td.classList.remove("hl");
  const rg = parseRange(range);
  if (!rg || !state.cells) return;
  let first = null;
  for (let r = rg.r1; r <= rg.r2; r++) {
    for (let c = rg.c1; c <= rg.c2; c++) {
      const td = state.cells.get(`${r},${c}`);
      if (td) {
        td.classList.add("hl");
        first = first || td;
      }
    }
  }
  if (first) {
    first.scrollIntoView({ block: "nearest", inline: "nearest", behavior: "smooth" });
    const wrap = $("#grid-wrap");
    const rect = wrap.getBoundingClientRect();
    if (rect.bottom < 0 || rect.top > window.innerHeight) wrap.scrollIntoView({ block: "start", behavior: "smooth" });
  }
}

function renderLoadState() {
  const enabled = state.config.loadEnabled && currentSheet() && currentSheet().tables.length > 0;
  $("#load-btn").hidden = !enabled;
  $("#load-btn").textContent = loading ? "Loading…" : state.load ? "Reload into Postgres" : "Load into Postgres";
  const loaded = !!state.load;
  $("#query-tab").hidden = !loaded;
  if (!loaded && state.tab === "query") state.tab = "clean";
  if (loaded) {
    const l = state.load;
    const names = l.tables.map((t) => `${t.table} (${t.rows} rows)`).join(", ");
    const exp = l.expiresAt ? ` It is dropped at ${new Date(l.expiresAt).toLocaleTimeString()}.` : "";
    $("#query-hint").textContent = l.queryEnabled
      ? `Loaded into schema ${l.schema}: ${names}. Queries run read-only with a 2 s timeout.${exp}`
      : `Loaded into schema ${l.schema}: ${names}. Querying is unavailable on this server.`;
    $("#run-sql").disabled = !l.queryEnabled;
  }
}

function resultTable(columns, rows) {
  const table = el("table", { class: "clean" });
  table.append(el("thead", {}, el("tr", {}, ...columns.map((c) => el("th", {}, el("span", { class: "col-name", text: c }))))));
  // Results arrive as text; right-align columns whose values are all numbers.
  const numeric = columns.map((_, j) => rows.some((r) => r[j] !== null) &&
    rows.every((r) => r[j] === null || /^-?\d+(\.\d+)?$/.test(r[j])));
  const body = el("tbody");
  for (const row of rows) {
    body.append(el("tr", {}, ...row.map((v, j) => v === null
      ? el("td", { class: "nullv", text: "NULL" })
      : el("td", { class: numeric[j] ? "num" : "", text: v }))));
  }
  table.append(body);
  return table;
}

function selectTab(name) {
  if (name === "query" && $("#query-tab").hidden) name = "clean";
  state.tab = name;
  for (const b of $$(".tabs [role=tab]")) b.setAttribute("aria-selected", String(b.dataset.tab === name));
  for (const p of $$(".tab-body")) p.hidden = p.id !== `tab-${name}`;
}

// ---- Wiring ---------------------------------------------------------------

function copyButton(btnSel, srcSel) {
  const btn = $(btnSel);
  btn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText($(srcSel).textContent);
      btn.textContent = "Copied";
      setTimeout(() => { btn.textContent = "Copy"; }, 1200);
    } catch { /* clipboard unavailable */ }
  });
}

function wire() {
  const drop = $("#drop");
  const input = $("#file");
  input.addEventListener("change", () => { if (input.files[0]) inspect({ file: input.files[0] }); input.value = ""; });
  drop.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); input.click(); } });
  for (const ev of ["dragenter", "dragover"]) drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add("over"); });
  for (const ev of ["dragleave", "drop"]) drop.addEventListener(ev, () => drop.classList.remove("over"));
  drop.addEventListener("drop", (e) => {
    e.preventDefault();
    const f = e.dataTransfer && e.dataTransfer.files[0];
    if (f) inspect({ file: f });
  });
  window.addEventListener("dragover", (e) => e.preventDefault());
  window.addEventListener("drop", (e) => e.preventDefault());

  for (const b of $$(".tabs [role=tab]")) b.addEventListener("click", () => selectTab(b.dataset.tab));
  for (const b of $$("#dialects button")) b.addEventListener("click", () => { state.dialect = b.dataset.dialect; renderDDL(currentTable()); });
  copyButton("#copy-ddl", "#ddl");
  copyButton("#copy-install", "#install-cmd");
  $("#load-btn").addEventListener("click", load);
  $("#run-sql").addEventListener("click", runQuery);
  $("#sql").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runQuery(); }
  });
  for (const sel of OPTIONS) {
    $(sel).addEventListener("change", () => { if (state.source) inspect(state.source); });
  }
}

async function init() {
  wire();
  try {
    state.config = await api("/api/config");
    const mb = Math.round(state.config.maxUploadBytes / (1 << 20));
    const rows = state.config.maxRows ? `, ${state.config.maxRows.toLocaleString()} rows` : "";
    $("#drop-sub").textContent = `or click to choose a file (up to ${mb} MB${rows}). It is processed in memory and never stored.`;
    if (!state.config.loadEnabled) $("#foot-retention").hidden = true;
    // Release builds report a semver; dev and Docker builds report a label.
    const v = state.config.version;
    if (v && /^v?\d+\.\d+/.test(v)) {
      const label = v.startsWith("v") ? v : `v${v}`;
      $("#version-badge").textContent = label;
      $("#version-badge").hidden = false;
      $("#foot-version").textContent = label;
    }
  } catch { /* keep defaults */ }
  try {
    const { samples } = await api("/api/samples");
    const list = $("#samples");
    for (const s of samples) {
      list.append(el("button", { class: "chip", type: "button", "data-sample": s.name, "aria-pressed": "false", title: s.description, text: s.name, onclick: () => inspect({ sample: s.name }) }));
    }
  } catch (err) {
    banner(`Could not load samples: ${err.message}`);
  }
}

init();
