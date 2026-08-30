// A pool of recycled <tr> over the visible window, two spacers holding the
// scroll height, only changed cells rewritten. Chosen and measured in
// docs/SPECS.md section 10.1.
"use strict";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const n0 = (v) => Math.round(v).toLocaleString("en-US");
const n2 = (v) => Number(v).toFixed(2);
const NA = '<span class="num na">n/a</span>';

// Dashboard formatters. Never called for an unavailable figure, so none of
// them has to invent a reading.
const fInt = (v) => n0(v);
const fNum1 = (v) => Number(v).toFixed(1);
const fPct = (v) => Math.round(v) + " %";
const fPct1 = (v) => Number(v).toFixed(1) + " %";
const fMB = (v) => (Math.abs(v) >= 1024 ? (v / 1024).toFixed(1) + " GB" : Number(v).toFixed(1) + " MB");
const fKB = (v) => fMB(v / 1024);
// The decimal is noise once the rate is large.
const fRate = (v) => (Math.abs(v) >= 100 ? n0(v) + "/s" : Number(v).toFixed(1) + "/s");
// Signed: a shrinking version store is the answer you are waiting for.
const fSigned = (v) => (v >= 0 ? "+" : "") + Number(v).toFixed(2) + " MB/s";

// Seconds to weeks, read the way a person reads a duration.
function fDur(sec) {
  sec = Math.max(0, Math.round(sec));
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  const p2 = (x) => String(x).padStart(2, "0");
  if (d) return d + "d " + p2(h) + "h";
  if (h) return h + "h " + p2(m) + "m";
  if (m) return m + "m " + p2(s) + "s";
  return s + "s";
}

const token = new URLSearchParams(location.search).get("t") || "";

// Per-session invariants, sent once. A row carries only the key; the SQL
// text, program, login and host live here rather than on every tick.
const refs = new Map();
function ref(key) {
  return refs.get(key) || { sql: "", prg: "", login: "", host: "" };
}

// Rows are positional arrays: the key names were a third of the payload at
// 800 rows. F maps name to index, from the header the server sends once, so
// the order lives only in protocol.go's rowFields.
let F = {};
function setCols(cols) {
  F = {};
  cols.forEach((name, i) => { F[name] = i; });
}
const val = (r, name) => r[F[name]];

// Includes the request id: under MARS one session runs several concurrent
// requests, and keying on the session alone loses the selection.
function rowKey(r) { return val(r, "spid") + ":" + val(r, "rqid"); }

// What the source can actually provide. Below SQL Server 2016 there is no
// dop column and without the tempdb right no tempdb figure; Go substitutes
// a literal 0 for both, so those columns are greyed rather than believed.
let caps = new Set();
function hasCap(name) { return caps.has(name); }

// Spec section 6's dashboard, in the order you read a misbehaving server:
// cpu, then memory, then arriving work, then what is holding on. Keys are
// SnapshotPayload.Figures keys. A key nobody sends renders like one sent
// unavailable, deliberately: at three in the morning the difference between
// "not collected" and "not answerable" changes nothing.
const FIG_GROUPS = [
  {
    title: "cpu and schedulers",
    tiles: [
      { key: "sql_cpu_percent", label: "sql server cpu", fmt: fPct },
      { key: "other_cpu_percent", label: "other processes", fmt: fPct },
      { key: "runnable_tasks", label: "runnable tasks", fmt: fInt },
      { key: "current_tasks", label: "current tasks", fmt: fInt },
      { key: "scheduler_load_factor", label: "load factor", fmt: fNum1 },
      { key: "schedulers_online", label: "schedulers", fmt: fInt },
    ],
  },
  {
    title: "memory",
    tiles: [
      { key: "total_server_memory_kb", label: "total server memory", fmt: fKB },
      { key: "target_server_memory_kb", label: "target server memory", fmt: fKB },
      { key: "buffer_pool_mb", label: "buffer pool", fmt: fMB },
      { key: "plan_cache_mb", label: "plan cache", fmt: fMB },
      { key: "query_memory_mb", label: "query memory", fmt: fMB },
      { key: "page_life_expectancy", label: "page life expectancy", fmt: fDur },
      // Windowed, not raw: the raw counter sits at 99-point-something on
      // every server forever. Page life expectancy is the one to trust.
      { key: "buffer_cache_hit_ratio", label: "cache hit ratio", fmt: fPct1 },
      { key: "memory_grants_pending", label: "grants pending", fmt: fInt },
      { key: "memory_grants_outstanding", label: "grants outstanding", fmt: fInt },
    ],
  },
  {
    title: "throughput",
    tiles: [
      { key: "active_requests", label: "active requests", fmt: fInt },
      { key: "batch_requests_sec", label: "batch requests", fmt: fRate },
      { key: "compilations_sec", label: "compilations", fmt: fRate },
      { key: "recompilations_sec", label: "recompilations", fmt: fRate },
      { key: "full_scans_sec", label: "full scans", fmt: fRate },
      { key: "page_reads_sec", label: "page reads", fmt: fRate },
      { key: "page_writes_sec", label: "page writes", fmt: fRate },
      { key: "lazy_writes_sec", label: "lazy writes", fmt: fRate },
    ],
  },
  {
    title: "transactions and tempdb",
    tiles: [
      { key: "open_transactions", label: "open transactions", fmt: fInt },
      { key: "longest_transaction_s", label: "longest transaction", fmt: fDur },
      { key: "tempdb_used_mb", label: "tempdb used", fmt: fMB },
      { key: "tempdb_free_mb", label: "tempdb free", fmt: fMB },
      { key: "tempdb_user_objects_mb", label: "tempdb user objects", fmt: fMB },
      { key: "tempdb_internal_objects_mb", label: "tempdb internal", fmt: fMB },
      { key: "tempdb_version_store_mb", label: "tempdb version store", fmt: fMB },
      { key: "version_store_mb", label: "version store", fmt: fMB },
      { key: "version_store_growth_mb_s", label: "version store growth", fmt: fSigned },
    ],
  },
];

// Not the retention window section 6 asks for: fifteen minutes is nine
// hundred points and a hundred-pixel sparkline draws that as a smear. It
// keeps what it can render and the panel heading states the span. The full
// window belongs in a chart in its own view, not a wider tile.
const HISTORY_MAX = 120;

// One series per key: parallel arrays of tick number and reading.
// Unavailable ticks append nothing, so a gap draws as one straight segment
// rather than compressing the time axis. All tiles share one x scale.
const history = new Map();
const tiles = new Map();
// Wall clock against tick numbers, so the heading can say what span it is
// showing: the period is not fixed, the budget slows the collector down.
const span = [];

const COLUMNS = [
  { field: "spid", title: "spid", width: 60, html: (r) => `<span class="num">${val(r, "spid")}</span>` },
  { field: "st", title: "status", width: 90 },
  { field: "db", title: "database", width: 110 },
  { field: "login", title: "login", width: 100, html: (r) => esc(ref(val(r, "ref")).login) },
  { field: "host", title: "host", width: 95, html: (r) => esc(ref(val(r, "ref")).host) },
  { field: "prg", title: "program", width: 200, html: (r) => esc(ref(val(r, "ref")).prg) },
  { field: "cmd", title: "command", width: 110 },
  { field: "w", title: "wait type", width: 150, html: (r) => waitBadge(val(r, "w")) },
  { field: "wms", title: "wait ms", width: 85, html: (r) => `<span class="num">${n0(val(r, "wms"))}</span>` },
  { field: "el", title: "elapsed ms", width: 100, html: (r) => `<span class="num">${n0(val(r, "el"))}</span>` },
  { field: "cpu", title: "cpu ms", width: 90, html: (r) => `<span class="num">${n0(val(r, "cpu"))}</span>` },
  { field: "rd", title: "reads", width: 95, html: (r) => `<span class="num">${n0(val(r, "rd"))}</span>` },
  { field: "wr", title: "writes", width: 90, html: (r) => `<span class="num">${n0(val(r, "wr"))}</span>` },
  { field: "tdb", title: "tempdb MB", width: 100, html: (r) => (hasCap("tempdbPerTask") ? `<span class="num">${n2(val(r, "tdb"))}</span>` : NA) },
  { field: "gr", title: "grant MB", width: 95, html: (r) => `<span class="num">${n2(val(r, "gr"))}</span>` },
  { field: "dop", title: "dop", width: 55, html: (r) => (hasCap("requestDOP") ? `<span class="num">${val(r, "dop")}</span>` : NA) },
  { field: "by", title: "blocked by", width: 95, html: (r) => (val(r, "by") ? `<span class="blocked">${val(r, "by")}</span>` : "") },
  { field: "sql", title: "SQL text", width: 520, html: (r) => `<span class="sqlcell" style="padding-left:${val(r, "d") * 14}px">${esc(ref(val(r, "ref")).sql)}</span>` },
];

function waitBadge(w) {
  if (!w) return "";
  let cls = "";
  if (w.startsWith("LCK_")) cls = " lck";
  else if (w.startsWith("PAGEIOLATCH") || w === "WRITELOG" || w === "ASYNC_NETWORK_IO") cls = " io";
  else if (w === "CXPACKET") cls = " cx";
  return `<span class="badge${cls}">${esc(w)}</span>`;
}

function cellHTML(col, row) {
  return col.html ? col.html(row) : esc(val(row, col.field) ?? "");
}

const ROW_H = 22;   // must match the height fixed in style.css
const OVERSCAN = 8; // rows rendered off screen, for clean scrolling
let data = [];
let selectedKey = null;
const pool = []; // {tr, tds, key, prev}
let spacerTop = null, spacerBottom = null;

// setup-region: begin
//
// The only two places this file writes markup outside the per-cell rewrite,
// and both run once from a fixed list. app_assets_test.go reads these two
// markers verbatim and takes the last of each, so there must be exactly one
// region: read that test before touching them.
function head() {
  $("gridHead").innerHTML = COLUMNS.map((c) => `<th scope="col" style="min-width:${c.width}px">${esc(c.title)}</th>`).join("");
}

// Lays the tiles out once and remembers the two nodes each one updates.
// After this the per-tick path writes text and one attribute per tile.
function buildDashboard() {
  $("dashBody").innerHTML = FIG_GROUPS.map((g) => `<section class="figGroup"><h2>${esc(g.title)}</h2><div class="figTiles">` +
    g.tiles.map((t) => `<div class="tile"><span class="tileLabel">${esc(t.label)}</span>` +
      `<span class="tileValue na" id="v-${esc(t.key)}">n/a</span>` +
      `<svg class="spark" viewBox="0 0 100 20" preserveAspectRatio="none" aria-hidden="true">` +
      `<polyline id="s-${esc(t.key)}" points=""></polyline></svg></div>`).join("") +
    `</div></section>`).join("");

  for (const g of FIG_GROUPS) {
    for (const t of g.tiles) {
      tiles.set(t.key, { def: t, value: $("v-" + t.key), spark: $("s-" + t.key) });
    }
  }
}
//
// setup-region: end

// The y scale is the series' own min to max, not an absolute one: a cache
// hit ratio on a 0-100 axis is a flat line at the top of every server ever
// built. A flat series draws through the middle, not along the floor where
// it would read as zero.
function sparkPoints(h, oldest, newest) {
  if (h.v.length < 2) return "";
  let min = Infinity, max = -Infinity;
  for (const v of h.v) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const width = Math.max(1, newest - oldest);
  const range = max - min;
  const out = new Array(h.v.length);
  for (let i = 0; i < h.v.length; i++) {
    const x = ((h.t[i] - oldest) / width) * 100;
    const y = range > 0 ? 18 - ((h.v[i] - min) / range) * 16 : 10;
    out[i] = x.toFixed(1) + "," + y.toFixed(1);
  }
  return out.join(" ");
}

// The dashboard's whole render path: text and attributes, never markup,
// and only what changed.
function updateDashboard(figures, seq, ts) {
  const oldest = seq - HISTORY_MAX + 1;

  span.push({ seq: seq, ts: ts });
  while (span.length && span[0].seq < oldest) span.shift();
  const seconds = span.length > 1 ? (span[span.length - 1].ts - span[0].ts) / 1000 : 0;
  const label = seconds > 0 ? " \u00b7 last " + fDur(seconds) : "";
  if ($("dashSpan").textContent !== label) $("dashSpan").textContent = label;

  for (const [key, t] of tiles) {
    const f = figures[key];
    const ok = !!(f && f.available);
    // Never falls back to the last value it had. A stale number that looks
    // live is the most convincing lie this page could tell.
    const text = ok ? t.def.fmt(f.value) : "n/a";
    if (t.value.textContent !== text) t.value.textContent = text;
    t.value.classList.toggle("na", !ok);

    let h = history.get(key);
    if (!h) {
      h = { t: [], v: [] };
      history.set(key, h);
    }
    if (ok) {
      h.t.push(seq);
      h.v.push(f.value);
    }
    while (h.t.length && h.t[0] < oldest) {
      h.t.shift();
      h.v.shift();
    }
    const pts = sparkPoints(h, oldest, seq);
    if (t.spark.getAttribute("points") !== pts) t.spark.setAttribute("points", pts);
  }
}

function ensureSpacers() {
  const body = $("gridBody");
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
    selectedKey = tr._key;
    for (const e of pool) e.tr.classList.toggle("sel", e.tr._key === selectedKey);
  });
  const entry = { tr, tds, key: null, prev: {} };
  pool.push(entry);
  $("gridBody").insertBefore(tr, spacerBottom);
  return entry;
}

// The grid's whole render path. It writes markup only on the one <td> whose
// content changed, never on a row, the body or the table. Rebuilding a row
// here or in a helper is invisible on review and costs the 4.8 ms figure
// this renderer exists for; app_assets_test.go guards it.
function layout() {
  const sc = document.querySelector(".gridScroll");
  if (!sc) return;
  ensureSpacers();

  const total = data.length;
  const visible = Math.ceil(sc.clientHeight / ROW_H) + OVERSCAN * 2;
  let first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - OVERSCAN);
  // Clamp to the last page the row count actually has. A storm clearing to
  // a handful while scrolled down otherwise leaves first past the end of
  // data, nothing renders, and the grid looks empty until the next scroll.
  first = Math.min(first, Math.max(0, total - visible));
  const count = Math.max(0, Math.min(total - first, visible));

  spacerTop.style.height = first * ROW_H + "px";
  spacerBottom.style.height = Math.max(0, (total - first - count) * ROW_H) + "px";

  while (pool.length < count) acquireRow();
  for (let i = count; i < pool.length; i++) pool[i].tr.hidden = true;

  for (let i = 0; i < count; i++) {
    const entry = pool[i];
    const r = data[first + i];
    const key = rowKey(r);
    entry.tr.hidden = false;
    if (entry.key !== key) { entry.key = key; entry.prev = {}; entry.tr._key = key; }
    for (let c = 0; c < COLUMNS.length; c++) {
      const col = COLUMNS[c];
      const html = cellHTML(col, r);
      if (entry.prev[col.field] !== html) {
        entry.tds[c].innerHTML = html;
        entry.prev[col.field] = html;
      }
    }
    entry.tr.classList.toggle("sel", key === selectedKey);
  }
}

// Instance start, Unix ms, zero when unreadable. The uptime ticks locally
// off it, so it keeps counting at one second on a throttled stream.
let startedAt = 0;

function infoRow(rowID, valueID, text) {
  const has = !!text;
  $(rowID).hidden = !has;
  if (has && $(valueID).textContent !== text) $(valueID).textContent = text;
}

function updateUptime() {
  if (!startedAt) {
    $("siUptime").hidden = true;
    return;
  }
  $("siUptime").hidden = false;
  $("infoUptime").textContent = fDur((Date.now() - startedAt) / 1000);
}

function applyStatus(st, seq) {
  const live = !!st.connected;
  $("dot").classList.toggle("live", live);
  // connState, not dot: a live region announces on a content change, not on
  // an accessible-name change, so an aria-label on dot is never read out.
  $("connState").textContent = live ? "connected" : "disconnected";
  if (st.sqltop) $("build").textContent = st.sqltop;
  $("instance").textContent = st.instance || "connecting...";
  $("version").textContent = st.version || "";
  infoRow("siHost", "infoHost", st.host || "");
  infoRow("siEdition", "infoEdition", st.edition || "");
  startedAt = st.startedAt || 0;
  updateUptime();
  $("infoRequests").textContent = n0(data.length);
  $("message").textContent = st.message || "";
  $("rowCount").textContent = data.length + " requests";
  $("seq").textContent = "tick " + seq;
  // Spec section 10: an instrument that claims to bound its own cost shows
  // it at all times, not only once it has already throttled itself.
  $("cost").textContent = "cost: " + Math.round(st.costMsPerSecond || 0) + " ms/s";
}

const es = new EventSource("/api/stream?t=" + encodeURIComponent(token));
es.addEventListener("snapshot", (e) => {
  const p = JSON.parse(e.data);
  // The column header comes once, on the first snapshot of a connection.
  if (p.cols) setCols(p.cols);
  data = p.rows || [];
  caps = new Set((p.status && p.status.caps) || []);

  // Prune on the same rule the server's Encoder uses: a key no row used
  // this tick is dropped. Without it a tab left open grows without bound,
  // measured at 1.3 MB an hour. A key that returns is resent.
  const alive = new Set(data.map((r) => val(r, "ref")));
  for (const k of refs.keys()) if (!alive.has(k)) refs.delete(k);
  if (p.refs) for (const [k, v] of Object.entries(p.refs)) refs.set(k, v);

  layout();
  applyStatus(p.status || {}, p.seq);

  // Section 6 lists active requests as counted from the grid data, free.
  // Injected into a copy so nothing mistakes it for a collector measurement.
  const figures = Object.assign({ active_requests: { value: data.length, unit: "", available: true } }, p.figures || {});
  updateDashboard(figures, p.seq, p.ts || Date.now());
});
es.addEventListener("error", () => {
  $("dot").classList.remove("live");
  $("connState").textContent = "disconnected";
  $("message").textContent = "lost the connection to sqltop, retrying";
});

document.querySelector(".gridScroll").addEventListener("scroll", layout, { passive: true });
// Otherwise enlarging the window leaves the new space empty until the next
// snapshot happens to call layout().
globalThis.addEventListener("resize", layout);

// Collapsing hands the dashboard's whole height to the grid: watching a
// blocking chain, you want the process list and nothing else. The toggle
// fires no resize event, hence the explicit layout().
//
// The choice is remembered. localStorage is per origin, so per port here,
// which beats inventing a preference file for one boolean. Both accesses
// are wrapped: a browser with site data blocked throws on the property
// itself, and an unhandled throw would take the startup path with it.
const DASH_KEY = "sqltop.dashboard.open";
try {
  const saved = localStorage.getItem(DASH_KEY);
  if (saved !== null) $("dashboard").open = saved === "1";
} catch { /* no stored preference is not a problem worth reporting */ }

$("dashboard").addEventListener("toggle", () => {
  try {
    localStorage.setItem(DASH_KEY, $("dashboard").open ? "1" : "0");
  } catch { /* a browser that will not store it still works, it just forgets */ }
  layout();
});
head();
buildDashboard();
// Counts locally: it keeps moving while the connection is down, which is
// honest, the instance is still up and this tool just cannot see it.
setInterval(updateUptime, 1000);
