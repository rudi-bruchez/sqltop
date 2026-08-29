// Rendering test bench. Four strategies, one set of columns, one instrumentation,
// so that the comparison is a fair fight.
"use strict";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const n0 = (v) => Math.round(v).toLocaleString("en-US");
const n2 = (v) => v.toFixed(2);

// ----------------------------------------------------------------- columns

// An inline SVG sparkline. This is the kind of cell that costs real time, so it
// has to be in the bench, otherwise the measurement is too optimistic.
function sparkline(hist) {
  const max = Math.max(1, ...hist);
  const w = 58, h = 13;
  const step = w / (hist.length - 1);
  let d = "";
  for (let i = 0; i < hist.length; i++) {
    d += (i ? " " : "") + (i * step).toFixed(1) + "," + (h - (hist[i] / max) * h).toFixed(1);
  }
  return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}"><polyline fill="none" stroke="#6fb3d2" stroke-width="1" points="${d}"/></svg>`;
}

function waitBadge(w) {
  if (!w) return "";
  let cls = "";
  if (w.startsWith("LCK_")) cls = " lck";
  else if (w.startsWith("PAGEIOLATCH") || w === "WRITELOG" || w === "ASYNC_NETWORK_IO") cls = " io";
  else if (w === "CXPACKET") cls = " cx";
  return `<span class="badge${cls}">${esc(w)}</span>`;
}

const COLUMNS = [
  { field: "spid", title: "spid", width: 60 },
  { field: "status", title: "status", width: 90 },
  { field: "db", title: "database", width: 100 },
  { field: "login", title: "login", width: 100 },
  { field: "host", title: "host", width: 95 },
  { field: "program", title: "program", width: 200 },
  { field: "command", title: "command", width: 100 },
  { field: "wait_type", title: "wait type", width: 150, html: (r) => waitBadge(r.wait_type) },
  { field: "wait_ms", title: "wait ms", width: 85, html: (r) => `<span class="num">${n0(r.wait_ms)}</span>` },
  { field: "dur_s", title: "elapsed s", width: 85, html: (r) => `<span class="num">${n0(r.dur_s)}</span>` },
  { field: "cpu_ms", title: "cpu ms", width: 90, html: (r) => `<span class="num">${n0(r.cpu_ms)}</span>` },
  { field: "cpu_hist", title: "cpu", width: 70, html: (r) => sparkline(r.cpu_hist) },
  { field: "reads_mb", title: "reads MB", width: 95, html: (r) => `<span class="num">${n2(r.reads_mb)}</span>` },
  { field: "writes_mb", title: "writes MB", width: 95, html: (r) => `<span class="num">${n2(r.writes_mb)}</span>` },
  { field: "tempdb_mb", title: "tempdb MB", width: 100, html: (r) => `<span class="num">${n2(r.tempdb_mb)}</span>` },
  { field: "grant_mb", title: "grant MB", width: 95, html: (r) => `<span class="num">${n0(r.grant_mb)}</span>` },
  { field: "dop", title: "dop", width: 55 },
  { field: "blocked_by", title: "blocked by", width: 95, html: (r) => (r.blocked_by ? `<span class="blocked">${r.blocked_by}</span>` : "") },
  { field: "pct", title: "%", width: 70, html: (r) => `<span class="bar"><i style="width:${r.pct}%"></i></span>` },
  { field: "sql", title: "SQL text", width: 460, html: (r) => `<span class="sqlcell">${esc(r.sql)}</span>` },
];

function cellHTML(col, row) {
  return col.html ? col.html(row) : esc(row[col.field] ?? "");
}

// ----------------------------------------------------------------- metrics

const M = {
  ticks: 0,
  apply: [],
  frame: [],
  jank: 0,
  jankMs: 0,
  scrollLost: 0,
  selLost: 0,
  rows: 0,
};

const KEEP = 200; // sliding window of samples

function record(applyMs, frameMs) {
  M.ticks++;
  M.apply.push(applyMs);
  M.frame.push(frameMs);
  if (M.apply.length > KEEP) { M.apply.shift(); M.frame.shift(); }
}

function pct(arr, p) {
  if (!arr.length) return null;
  const s = [...arr].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))];
}

const fmtMs = (v) => (v === null ? "-" : v.toFixed(1) + " ms");

function paintStats() {
  $("sTicks").textContent = M.ticks;
  $("sRows").textContent = M.rows;
  $("sApply50").textContent = fmtMs(pct(M.apply, 50));
  $("sApply95").textContent = fmtMs(pct(M.apply, 95));
  $("sApplyMax").textContent = fmtMs(M.apply.length ? Math.max(...M.apply) : null);
  $("sFrame95").textContent = fmtMs(pct(M.frame, 95));
  $("sFreeze").textContent = M.jank + (M.jankMs ? ` (${Math.round(M.jankMs)} ms)` : "");
  $("sScroll").textContent = M.scrollLost;
  $("sSel").textContent = M.selLost;
  if (performance.memory) {
    $("sHeap").textContent = Math.round(performance.memory.usedJSHeapSize / 1048576) + " MB";
  }
}

function resetStats() {
  M.ticks = 0; M.apply = []; M.frame = []; M.jank = 0; M.jankMs = 0;
  M.scrollLost = 0; M.selLost = 0;
  paintStats();
}

// Browser-independent freeze detection. PerformanceObserver's longtask entry
// type does not exist in Firefox, where its counter always reads zero and gives
// a false impression of smoothness. So we measure the interval between painted
// frames directly: past 50 ms, the interface has stalled.
const JANK_MS = 50;
let lastFrameAt = performance.now();
(function watchFrames(now) {
  const gap = now - lastFrameAt;
  if (gap > JANK_MS) { M.jank++; M.jankMs += gap; }
  lastFrameAt = now;
  requestAnimationFrame(watchFrames);
})(performance.now());

// ------------------------------------------------------------------- state

let mode = "replaceData";
let treeMode = false;
// fitDataFill re-measures the natural width of every column on each redraw.
// Since every column has an explicit width, that is pure overhead.
let layoutMode = "fitDataFill";
let table = null;          // Tabulator instance
let selectedSpid = null;   // row tracked by the selection test
let dataById = new Map();  // current state, used by delta mode and by the plain renderer
let es = null;
let autoScrolling = false;

// The blocking tree is built client side from blocked_by: the wire protocol
// stays flat, exactly as the real collector will keep it.
function buildTree(rows) {
  const byId = new Map(rows.map((r) => [r.spid, { ...r, _children: undefined }]));
  const roots = [];
  for (const r of byId.values()) {
    const parent = r.blocked_by ? byId.get(r.blocked_by) : null;
    if (parent && parent !== r) {
      (parent._children ||= []).push(r);
    } else {
      roots.push(r);
    }
  }
  return roots;
}

function scrollContainer() {
  return mode === "plain"
    ? document.querySelector(".plainScroll")
    : document.querySelector(".tabulator-tableholder");
}

// --------------------------------------------------------------- Tabulator

function destroyTable() {
  if (table) { table.destroy(); table = null; }
}

function buildTable() {
  destroyTable();
  const columns = COLUMNS.map((c) => ({
    title: c.title,
    field: c.field,
    width: c.width,
    headerSort: true,
    resizable: true,
    ...(c.html ? { formatter: (cell) => c.html(cell.getRow().getData()) } : {}),
  }));

  table = new Tabulator("#grid", {
    height: "62vh",
    layout: layoutMode,
    index: "spid",
    columns,
    data: [],
    selectableRows: 1,
    reactiveData: false,
    dataTree: treeMode,
    dataTreeChildField: "_children",
    dataTreeStartExpanded: true,
    renderVertical: "virtual",
  });

  table.on("rowClick", (e, row) => {
    selectedSpid = row.getData().spid;
    row.select();
  });
}

// -------------------------------------------------------- hand-rolled table

function buildPlainHead() {
  $("plainHead").innerHTML = COLUMNS.map((c) => `<th style="min-width:${c.width}px">${esc(c.title)}</th>`).join("");
}

// Virtualised hand-rolled renderer: a pool of recycled <tr> covering only the
// visible window, two spacers to hold the scroll height, and a rewrite of only
// those cells whose rendering changed. This is what a serious implementation
// would look like; without the virtualisation the comparison against Tabulator
// is rigged, since Tabulator only ever paints one screenful.
const ROW_H = 22;    // must match the height fixed in CSS
const OVERSCAN = 8;  // rows rendered off screen, for clean scrolling

let plainData = [];
const pool = [];     // {tr, tds, spid, prev}
let spacerTop = null, spacerBottom = null;

function ensureSpacers() {
  const body = $("plainBody");
  if (!spacerTop) {
    spacerTop = document.createElement("tr");
    spacerTop.appendChild(document.createElement("td")).colSpan = COLUMNS.length;
    body.appendChild(spacerTop);
  }
  if (!spacerBottom) {
    spacerBottom = document.createElement("tr");
    spacerBottom.appendChild(document.createElement("td")).colSpan = COLUMNS.length;
    body.appendChild(spacerBottom);
  }
}

function acquireRow() {
  const tr = document.createElement("tr");
  const tds = COLUMNS.map(() => tr.appendChild(document.createElement("td")));
  tr.addEventListener("click", () => {
    selectedSpid = tr._spid;
    for (const e of pool) e.tr.classList.toggle("sel", e.tr._spid === selectedSpid);
  });
  const entry = { tr, tds, spid: null, prev: {} };
  pool.push(entry);
  $("plainBody").insertBefore(tr, spacerBottom);
  return entry;
}

function layoutPlain() {
  const sc = document.querySelector(".plainScroll");
  if (!sc) return;
  ensureSpacers();

  const total = plainData.length;
  const first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - OVERSCAN);
  const visible = Math.ceil(sc.clientHeight / ROW_H) + OVERSCAN * 2;
  const count = Math.max(0, Math.min(total - first, visible));

  spacerTop.style.height = first * ROW_H + "px";
  spacerBottom.style.height = Math.max(0, (total - first - count) * ROW_H) + "px";

  while (pool.length < count) acquireRow();
  for (let i = count; i < pool.length; i++) pool[i].tr.hidden = true;

  for (let i = 0; i < count; i++) {
    const entry = pool[i];
    const r = plainData[first + i];
    entry.tr.hidden = false;
    // Row recycled for a different spid: every cell has to be rewritten.
    if (entry.spid !== r.spid) { entry.spid = r.spid; entry.prev = {}; entry.tr._spid = r.spid; }
    for (let c = 0; c < COLUMNS.length; c++) {
      const col = COLUMNS[c];
      const html = cellHTML(col, r);
      if (entry.prev[col.field] !== html) {
        entry.tds[c].innerHTML = html;
        entry.prev[col.field] = html;
      }
    }
    entry.tr.classList.toggle("sel", r.spid === selectedSpid);
  }
}

function renderPlain(rows) {
  plainData = rows;
  layoutPlain();
}

function resetPlain() {
  plainData = [];
  pool.length = 0;
  spacerTop = spacerBottom = null;
  $("plainBody").innerHTML = "";
}

// ----------------------------------------------------------------- render

function applySnapshot(rows) {
  dataById = new Map(rows.map((r) => [r.spid, r]));
  M.rows = rows.length;
  const payload = treeMode && mode !== "plain" ? buildTree(rows) : rows;

  if (mode === "plain") return renderPlain(rows);
  if (mode === "setData") return table.setData(payload);
  return table.replaceData(payload);
}

function applyDelta(d) {
  for (const spid of d.remove || []) dataById.delete(spid);
  for (const r of d.upsert || []) dataById.set(r.spid, r);
  M.rows = dataById.size;

  const jobs = [];
  if (d.remove && d.remove.length) {
    for (const spid of d.remove) {
      const row = table.getRow(spid);
      if (row) jobs.push(row.delete());
    }
  }
  if (d.upsert && d.upsert.length) jobs.push(table.updateOrAddData(d.upsert));
  return Promise.all(jobs);
}

// Measurement: synchronous apply time, then time to the painted frame.
// Checks: did the scroll position and the selection survive.
function measured(fn) {
  const sc = scrollContainer();
  const before = sc ? sc.scrollTop : 0;
  const t0 = performance.now();

  Promise.resolve(fn()).then(() => {
    const applyMs = performance.now() - t0;
    requestAnimationFrame(() => {
      record(applyMs, performance.now() - t0);

      // Continuous scrolling moves the position all the time: naively comparing
      // before and after would count that legitimate movement. So we only flag
      // the true failure signature, a hard jump back to the top of the list, and
      // free drift only when nothing is scrolling on its own.
      const sc2 = scrollContainer();
      if (sc2) {
        const after = sc2.scrollTop;
        const resetToTop = before > 50 && after < 5;
        const jumped = !autoScrolling && before > 0 && Math.abs(after - before) > 4;
        if (resetToTop || jumped) M.scrollLost++;
      }

      if (selectedSpid !== null && dataById.has(selectedSpid)) {
        if (mode === "plain") {
          // Outside the visible window the row is not rendered, so nothing can
          // be concluded: only observable losses are counted.
          const entry = pool.find((e) => e.spid === selectedSpid && !e.tr.hidden);
          if (entry && !entry.tr.classList.contains("sel")) M.selLost++;
        } else if (!table.getSelectedData().some((r) => r.spid === selectedSpid)) {
          M.selLost++;
        }
      }
      paintStats();
    });
  });
}

// ------------------------------------------------------------------ stream

function feedForMode() { return mode === "updateData" ? "delta" : "snapshot"; }

function connect() {
  if (es) es.close();
  es = new EventSource("/stream?feed=" + feedForMode());

  es.addEventListener("open", () => {
    $("dot").classList.add("live");
    $("connState").textContent = "connected (" + feedForMode() + ")";
  });
  es.addEventListener("error", () => {
    $("dot").classList.remove("live");
    $("connState").textContent = "reconnecting...";
  });
  es.addEventListener("snapshot", (e) => {
    const s = JSON.parse(e.data);
    measured(() => applySnapshot(s.rows));
  });
  es.addEventListener("delta", (e) => {
    const d = JSON.parse(e.data);
    measured(() => applyDelta(d));
  });
}

// ----------------------------------------------------------------- controls

function switchMode(next) {
  mode = next;
  const plainOn = mode === "plain";
  $("plain").hidden = !plainOn;
  $("grid").hidden = plainOn;

  // The tree makes no sense in delta mode: Tabulator moves a child back to the
  // root when its parent changes, so the whole tree would have to be rebuilt on
  // every tick, which cancels the benefit of the delta. That is a bench result.
  const treeBox = $("tree");
  treeBox.disabled = mode === "updateData" || plainOn;
  if (treeBox.disabled && treeBox.checked) { treeBox.checked = false; treeMode = false; }

  resetPlain();
  if (plainOn) { destroyTable(); } else { buildTable(); }

  selectedSpid = null;
  resetStats();
  connect();
}

function control(params) {
  fetch("/control?" + new URLSearchParams(params)).catch(() => {});
}

document.querySelectorAll('input[name=mode]').forEach((el) => {
  el.addEventListener("change", () => switchMode(el.value));
});

$("layout").addEventListener("change", (e) => {
  layoutMode = e.target.value;
  if (mode !== "plain") { buildTable(); resetStats(); }
});

$("tree").addEventListener("change", (e) => {
  treeMode = e.target.checked;
  if (mode !== "plain") { buildTable(); resetStats(); }
});

for (const id of ["rows", "hz", "churn"]) {
  const el = $(id);
  el.addEventListener("input", () => {
    $(id + "Out").textContent = el.value;
    control({ [id]: el.value });
  });
}

$("reset").addEventListener("click", resetStats);

$("preset").addEventListener("click", () => {
  for (const [id, val] of [["rows", "300"], ["hz", "1"], ["churn", "5"]]) {
    $(id).value = val;
    $(id + "Out").textContent = val;
  }
  control({ rows: 300, hz: 1, churn: 5 });
  resetStats();
});

$("copy").addEventListener("click", () => {
  const report = [
    "sqltop / rendering bench",
    "mode         : " + mode + (mode === "plain" ? "" : " / " + layoutMode) + (treeMode ? " + tree" : ""),
    "load         : " + $("rows").value + " rows, " + $("hz").value + " Hz, churn " + $("churn").value + " %",
    "ticks        : " + M.ticks,
    "apply p50    : " + fmtMs(pct(M.apply, 50)),
    "apply p95    : " + fmtMs(pct(M.apply, 95)),
    "apply max    : " + fmtMs(M.apply.length ? Math.max(...M.apply) : null),
    "frame p95    : " + fmtMs(pct(M.frame, 95)),
    "freezes>50ms : " + M.jank + " (" + Math.round(M.jankMs) + " ms total)",
    "scroll lost  : " + M.scrollLost,
    "selection    : " + M.selLost + " lost",
    "browser      : " + navigator.userAgent,
  ].join("\n");
  navigator.clipboard.writeText(report).then(
    () => { $("copy").textContent = "copied"; setTimeout(() => ($("copy").textContent = "copy report"), 1200); },
    () => console.log(report)
  );
});

// Continuous scrolling: the worst case is refreshing while the user is
// scrolling the grid.
let scrollTimer = null, scrollDir = 1;
$("autoscroll").addEventListener("change", (e) => {
  clearInterval(scrollTimer);
  autoScrolling = e.target.checked;
  if (!autoScrolling) return;
  // Stay inside the middle band, so that a scrollTop near zero can only come
  // from the grid resetting it, never from the bench itself.
  scrollTimer = setInterval(() => {
    const sc = scrollContainer();
    if (!sc) return;
    const span = sc.scrollHeight - sc.clientHeight;
    if (span <= 0) return;
    const low = span * 0.2, high = span * 0.8;
    if (sc.scrollTop < low) sc.scrollTop = low;
    sc.scrollTop += scrollDir * 14;
    if (sc.scrollTop <= low || sc.scrollTop >= high) scrollDir *= -1;
  }, 32);
});

document.querySelector(".plainScroll").addEventListener("scroll", () => {
  if (mode === "plain") layoutPlain();
}, { passive: true });

buildPlainHead();
switchMode("replaceData");
setInterval(paintStats, 500);
