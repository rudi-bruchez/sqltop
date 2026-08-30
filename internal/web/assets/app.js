// A pool of recycled <tr> over the visible window, two spacers holding the
// scroll height, only changed cells rewritten. Chosen and measured in
// docs/SPECS.md section 10.1.
"use strict";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
// One reused Intl.NumberFormat, not Number.prototype.toLocaleString, which
// builds a formatter on every call. Profiled at 800 rows this was 115 ms of
// the 1.4 s of JavaScript in a 45 second run, second only to layout itself
// and about half the cost of a refresh.
const nfInt = new Intl.NumberFormat("en-US");
const n0 = (v) => nfInt.format(Math.round(v));
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

// The dashboard's shape comes from the server, once per connection: which
// groups, in what order, with which figures and what to call them. That is
// resolved there from the configuration file, so one place decides what a
// partial file means and the catalogue lives in one language rather than
// two. What stays here is how to draw a number, which Go has no business
// knowing.
//
// A key with no formatter falls back to a plain integer rather than
// throwing, so a figure added to the catalogue renders sensibly before
// anybody thinks about its units.
const FMT = {
  sql_cpu_percent: fPct,
  other_cpu_percent: fPct,
  runnable_tasks: fInt,
  current_tasks: fInt,
  scheduler_load_factor: fNum1,
  schedulers_online: fInt,
  total_server_memory_kb: fKB,
  target_server_memory_kb: fKB,
  buffer_pool_mb: fMB,
  plan_cache_mb: fMB,
  query_memory_mb: fMB,
  page_life_expectancy: fDur,
  // Windowed, not raw: the raw counter sits at 99-point-something on every
  // server forever. Page life expectancy is the one to trust.
  buffer_cache_hit_ratio: fPct1,
  memory_grants_pending: fInt,
  memory_grants_outstanding: fInt,
  active_requests: fInt,
  batch_requests_sec: fRate,
  compilations_sec: fRate,
  recompilations_sec: fRate,
  full_scans_sec: fRate,
  page_reads_sec: fRate,
  page_writes_sec: fRate,
  lazy_writes_sec: fRate,
  open_transactions: fInt,
  longest_transaction_s: fDur,
  tempdb_used_mb: fMB,
  tempdb_free_mb: fMB,
  tempdb_user_objects_mb: fMB,
  tempdb_internal_objects_mb: fMB,
  tempdb_version_store_mb: fMB,
  version_store_mb: fMB,
  version_store_growth_mb_s: fSigned,
};

// FIG_GROUPS is what the server last told us to draw. Empty until the first
// snapshot arrives, which is also when the grid's columns arrive.
let FIG_GROUPS = [];

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

// Every column carries three things: how to render it, how to read its raw
// value for sorting and filtering, and whether that value is a number.
// Rendering and comparing are separate on purpose: "1,234" sorts as text
// between "1,2" and "1,3", and n/a is not a small number.
const COLUMNS = [
  { field: "spid", title: "spid", width: 60, num: true, get: (r) => val(r, "spid"), html: (r) => `<span class="num">${val(r, "spid")}</span>` },
  { field: "st", title: "status", width: 90, get: (r) => val(r, "st") },
  { field: "db", title: "database", width: 110, get: (r) => val(r, "db") },
  { field: "login", title: "login", width: 100, get: (r) => ref(val(r, "ref")).login, html: (r) => esc(ref(val(r, "ref")).login) },
  { field: "host", title: "host", width: 95, get: (r) => ref(val(r, "ref")).host, html: (r) => esc(ref(val(r, "ref")).host) },
  { field: "prg", title: "program", width: 200, get: (r) => ref(val(r, "ref")).prg, html: (r) => esc(ref(val(r, "ref")).prg) },
  { field: "cmd", title: "command", width: 110, get: (r) => val(r, "cmd") },
  { field: "w", title: "wait type", width: 150, get: (r) => val(r, "w"), html: (r) => waitBadge(val(r, "w")) },
  { field: "wms", title: "wait ms", width: 85, num: true, get: (r) => val(r, "wms"), html: (r) => `<span class="num">${n0(val(r, "wms"))}</span>` },
  { field: "el", title: "elapsed ms", width: 100, num: true, get: (r) => val(r, "el"), html: (r) => `<span class="num">${n0(val(r, "el"))}</span>` },
  { field: "cpu", title: "cpu ms", width: 90, num: true, get: (r) => val(r, "cpu"), html: (r) => `<span class="num">${n0(val(r, "cpu"))}</span>` },
  { field: "rd", title: "reads", width: 95, num: true, get: (r) => val(r, "rd"), html: (r) => `<span class="num">${n0(val(r, "rd"))}</span>` },
  { field: "wr", title: "writes", width: 90, num: true, get: (r) => val(r, "wr"), html: (r) => `<span class="num">${n0(val(r, "wr"))}</span>` },
  { field: "tdb", title: "tempdb MB", width: 100, num: true, get: (r) => val(r, "tdb"), html: (r) => (hasCap("tempdbPerTask") ? `<span class="num">${n2(val(r, "tdb"))}</span>` : NA) },
  { field: "gr", title: "grant MB", width: 95, num: true, get: (r) => val(r, "gr"), html: (r) => `<span class="num">${n2(val(r, "gr"))}</span>` },
  { field: "dop", title: "dop", width: 55, num: true, get: (r) => val(r, "dop"), html: (r) => (hasCap("requestDOP") ? `<span class="num">${val(r, "dop")}</span>` : NA) },
  { field: "by", title: "blocked by", width: 95, num: true, get: (r) => val(r, "by"), html: (r) => (val(r, "by") ? `<span class="blocked">${val(r, "by")}</span>` : "") },
  { field: "sql", title: "SQL text", width: 520, get: (r) => ref(val(r, "ref")).sql, html: (r) => `<span class="sqlcell" style="padding-left:${val(r, "d") * 14}px">${esc(ref(val(r, "ref")).sql)}</span>` },
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

// Sort and filter, spec section 8.1. Both run in the browser on data already
// in the retention window, which section 10.1 settled by measuring every
// client-side candidate against a server-side twin: the pairs do not
// separate, and doing it in Go would cost a filter per viewer, a filter over
// the history rather than over what was ever collected, and the ability to
// tell a row that ended from a row that stopped matching.
//
// Debt: this state belongs in the named layout the server owns, spec section
// 8.2, together with the folded groups above. Layouts do not exist yet.
let sortField = null;
let sortDir = 0; // 1 ascending, -1 descending, 0 unsorted
const filters = new Map(); // field -> the raw text the user typed

// filter-logic: begin
//
// parseFilter and matches are pure: no DOM, no state, no closure over
// anything. web/filter_logic_test.go lifts this region out between these two
// markers and runs it through deno against a table of cases, so the five
// operators of section 8.1 are checked rather than tried once in a browser.
// Moving either marker breaks that test loudly.
//
// parseFilter turns one typed box into one of the five operators of section
// 8.1. The operator is inferred from what was typed rather than chosen from
// a dropdown per column, because eighteen dropdowns is a lot of interface
// for a grid, and > < = are what a person types anyway.
//
//	>1000       greater than, numeric
//	<50         less than, numeric
//	=SELECT     equals, exact but case-insensitive
//	CRM,Ventes  in, any of the comma-separated values
//	UPDATE      contains, the default
function parseFilter(text) {
  const t = text.trim();
  if (t === "") return null;
  for (const op of [">", "<", "="]) {
    if (t.startsWith(op)) {
      const rest = t.slice(op.length).trim();
      if (rest === "") return null;
      const n = Number(rest);
      return { op: op, text: rest.toLowerCase(), num: Number.isFinite(n) ? n : null };
    }
  }
  if (t.includes(",")) {
    const parts = t.split(",").map((x) => x.trim().toLowerCase()).filter((x) => x !== "");
    return parts.length ? { op: "in", values: parts } : null;
  }
  return { op: "contains", text: t.toLowerCase() };
}

// matches applies one parsed filter to one raw value. Text comparison is
// case-insensitive throughout: a DBA hunting a runaway query is not also
// spelling a database name in the right case.
function matches(f, raw) {
  if (f.op === ">" || f.op === "<") {
    // A numeric comparison against a column that holds text is not an
    // error worth reporting, it simply matches nothing.
    if (f.num === null) return false;
    const v = Number(raw);
    if (!Number.isFinite(v)) return false;
    return f.op === ">" ? v > f.num : v < f.num;
  }
  const sv = String(raw ?? "").toLowerCase();
  if (f.op === "=") return sv === f.text;
  if (f.op === "in") return f.values.includes(sv);
  return sv.includes(f.text);
}

//
// filter-logic: end

// applyView filters then sorts. Filters on different columns combine with
// AND; several values on one column through `in` combine with OR, which is
// what `in` means. Section 8.1 says there is no interface for expressing
// anything else, deliberately.
function applyView(rows) {
  let out = rows;

  const active = [];
  for (const [field, text] of filters) {
    const f = parseFilter(text);
    if (!f) continue;
    const col = COLUMNS.find((c) => c.field === field);
    if (col) active.push({ f: f, get: col.get });
  }
  if (active.length) {
    out = out.filter((r) => active.every((a) => matches(a.f, a.get(r))));
  }

  if (sortField && sortDir) {
    const col = COLUMNS.find((c) => c.field === sortField);
    if (col) {
      // A copy, because `data` is the array the snapshot handler owns and
      // sorting in place would reorder it under the next tick's diff.
      out = out.slice().sort((a, b) => {
        const x = col.get(a), y = col.get(b);
        if (col.num) return (Number(x) - Number(y)) * sortDir;
        return String(x ?? "").localeCompare(String(y ?? ""), "en", { sensitivity: "base" }) * sortDir;
      });
    }
  }
  return out;
}

// setSort cycles a column: unsorted, ascending, descending, unsorted. A
// third click clearing the sort matters because the unsorted order is the
// server's, which is the blocking chain order the grid is built around.
function setSort(field) {
  if (sortField !== field) {
    sortField = field;
    sortDir = 1;
  } else if (sortDir === 1) {
    sortDir = -1;
  } else {
    sortField = null;
    sortDir = 0;
  }
  markSort();
  refresh();
}

function markSort() {
  for (const c of COLUMNS) {
    const el = $("sort-" + c.field);
    if (!el) continue;
    const mark = c.field === sortField ? (sortDir === 1 ? " \u25b2" : " \u25bc") : "";
    if (el.textContent !== mark) el.textContent = mark;
  }
}

// refresh recomputes the view and redraws. Called on a tick, and whenever
// the sort or a filter changes, which is why it is separate from the
// snapshot handler.
//
// keepSelection is what section 8.1 asks for: changing a filter re-anchors
// on the selected row when it survives, and goes to the top when it does
// not. The bench measured five lost scroll positions out of 122 ticks when
// 800 rows became 110 while scrolled toward the bottom, which is the
// content becoming shorter than the offset and the browser clamping it.
function refresh(keepSelection) {
  view = applyView(data);
  if (keepSelection) anchor();
  layout();
  $("rowCount").textContent = n0(view.length) + (view.length === data.length ? " requests" : " of " + n0(data.length) + " requests");
}

function anchor() {
  const sc = document.querySelector(".gridScroll");
  if (!sc) return;
  const i = selectedKey === null ? -1 : view.findIndex((r) => rowKey(r) === selectedKey);
  if (i < 0) {
    sc.scrollTop = 0;
    return;
  }
  // Put it a third of the way down rather than at the very top, so the rows
  // around it, which is the reason it was selected, stay on screen.
  sc.scrollTop = Math.max(0, i * ROW_H - sc.clientHeight / 3);
}

const ROW_H = 22;   // must match the height fixed in style.css
const OVERSCAN = 8; // rows rendered off screen, for clean scrolling
let data = [];   // what the server sent this tick
let view = [];   // what the grid draws: data, filtered and sorted
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
  $("gridHead").innerHTML = COLUMNS.map((c) =>
    `<th scope="col" style="min-width:${c.width}px"><button type="button" class="sortBtn" data-f="${esc(c.field)}" ` +
    `title="sort by ${esc(c.title)}">${esc(c.title)}<span class="sortMark" id="sort-${esc(c.field)}"></span></button></th>`).join("");

  // A second header row of filter boxes. One box per column, and the
  // operator is read from what is typed: see parseFilter.
  $("gridFilter").innerHTML = COLUMNS.map((c) =>
    `<th><span class="filterWrap"><input class="filterBox" data-f="${esc(c.field)}" id="f-${esc(c.field)}" ` +
    `aria-label="filter ${esc(c.title)}" placeholder="${c.num ? ">1000" : "filter"}">` +
    `<button type="button" class="filterClear" data-f="${esc(c.field)}" id="x-${esc(c.field)}" ` +
    `aria-label="clear the ${esc(c.title)} filter" title="clear" hidden>\u00d7</button></span></th>`).join("");

  for (const el of document.querySelectorAll(".sortBtn")) {
    el.addEventListener("click", () => setSort(el.dataset.f));
  }
  for (const el of document.querySelectorAll(".filterBox")) {
    el.addEventListener("input", () => setFilter(el, el.value));
    // Escape clears the box it is in, which is the gesture people try.
    el.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && el.value !== "") setFilter(el, "");
    });
  }
  // A cross inside the box, shown only while there is something to clear.
  for (const x of document.querySelectorAll(".filterClear")) {
    x.addEventListener("click", () => {
      const el = $("f-" + x.dataset.f);
      setFilter(el, "");
      el.focus();
    });
  }
}

// Lays the tiles out once and remembers the two nodes each one updates.
// After this the per-tick path writes text and one attribute per tile.
function buildDashboard(groups) {
  FIG_GROUPS = groups || [];
  tiles.clear();
  history.clear();
  $("dashBody").innerHTML = FIG_GROUPS.map((g) =>
    `<details class="figGroup" id="g-${esc(g.id)}" ${g.folded ? "" : "open"}><summary>${esc(g.title)}</summary><div class="figTiles">` +
    (g.figures || []).map((t) => `<div class="tile"><span class="tileLabel">${esc(t.label)}</span>` +
      `<span class="tileValue na" id="v-${esc(t.key)}">n/a</span>` +
      `<svg class="spark" viewBox="0 0 100 20" preserveAspectRatio="none" aria-hidden="true">` +
      `<polyline id="s-${esc(t.key)}" points=""></polyline></svg></div>`).join("") +
    `</div></details>`).join("");

  for (const g of FIG_GROUPS) {
    for (const t of g.figures || []) {
      tiles.set(t.key, { fmt: FMT[t.key] || fInt, value: $("v-" + t.key), spark: $("s-" + t.key) });
    }
    // The configuration says whether a group starts folded; what the user
    // then does with it is remembered and wins from that point.
    remember($("g-" + g.id), "sqltop.group." + g.id);
  }
}
//
// setup-region: end

// setFilter is the single place a filter box changes. Typing, Escape and the
// clear button all go through it, so the value, the highlight, the clear
// button's visibility and the filters map cannot drift apart.
function setFilter(el, value) {
  const field = el.dataset.f;
  el.value = value;
  if (value.trim() === "") filters.delete(field);
  else filters.set(field, value);
  const on = filters.has(field);
  el.classList.toggle("on", on);
  const x = $("x-" + field);
  if (x) x.hidden = !on;
  refresh(true);
}

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
    const text = ok ? t.fmt(f.value) : "n/a";
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

  const total = view.length;
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
    const r = view[first + i];
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
  // The full product version, next to the edition where section 6 puts it.
  // It used to sit dimmed beside the instance name, which is where nobody
  // looked for it. No marketing name derived from it: 12.0.x is either SQL
  // Server 2014 or one of the Azure engines, and this side of the wire
  // cannot tell which.
  infoRow("siVersion", "infoVersion", st.version || "");
  infoRow("siDeployment", "infoDeployment", st.deployment || "");
  infoRow("siHost", "infoHost", st.host || "");
  infoRow("siEdition", "infoEdition", st.edition || "");
  startedAt = st.startedAt || 0;
  updateUptime();
  $("infoRequests").textContent = n0(data.length);
  $("message").textContent = st.message || "";
  $("seq").textContent = "tick " + seq;
  // Spec section 10: an instrument that claims to bound its own cost shows
  // it at all times, not only once it has already throttled itself.
  $("cost").textContent = "cost: " + Math.round(st.costMsPerSecond || 0) + " ms/s";
}

const es = new EventSource("/api/stream?t=" + encodeURIComponent(token));
es.addEventListener("snapshot", (e) => {
  const p = JSON.parse(e.data);
  // The column header and the dashboard shape both come once, on the first
  // snapshot of a connection.
  if (p.cols) setCols(p.cols);
  if (p.dash) buildDashboard(p.dash);
  data = p.rows || [];
  caps = new Set((p.status && p.status.caps) || []);

  // Prune on the same rule the server's Encoder uses: a key no row used
  // this tick is dropped. Without it a tab left open grows without bound,
  // measured at 1.3 MB an hour. A key that returns is resent.
  const alive = new Set(data.map((r) => val(r, "ref")));
  for (const k of refs.keys()) if (!alive.has(k)) refs.delete(k);
  if (p.refs) for (const [k, v] of Object.entries(p.refs)) refs.set(k, v);

  refresh();
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

// Collapsing hands height back to the grid: watching a blocking chain, you
// want the process list and nothing else. A <details> toggle fires no resize
// event, hence the explicit layout(). The same applies one level down, per
// group, so a dashboard can be trimmed to the two groups that matter today
// rather than only all-or-nothing.
//
// Debt: spec section 8.2 says this state belongs in the named layout the
// server owns, not in the browser. Layouts do not exist yet, so this is
// localStorage, which is per origin and therefore per port. The way out is
// the layout file, and when it lands these keys are what it replaces.
function remember(el, key) {
  try {
    const saved = localStorage.getItem(key);
    if (saved !== null) el.open = saved === "1";
  } catch { /* no stored preference is not worth reporting */ }
  el.addEventListener("toggle", () => {
    try {
      localStorage.setItem(key, el.open ? "1" : "0");
    } catch { /* a browser that will not store it still works, it forgets */ }
    layout();
  });
}

remember($("dashboard"), "sqltop.dashboard.open");
head();
// Counts locally: it keeps moving while the connection is down, which is
// honest, the instance is still up and this tool just cannot see it.
setInterval(updateUptime, 1000);
