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
//
// Keyed by the readable field name of spec section 8.1, which is also what
// the configuration file uses. The terse names inside val() are the wire's:
// the wire spends its bytes 800 times a second, the file is read by a
// person. internal/web/catalogue_test.go checks these keys against
// model.ViewCatalogue so neither half can gain a column the other lacks.
const CELL_REQUESTS = {
  spid: { num: true, get: (r) => val(r, "spid"), html: (r) => `<span class="num">${val(r, "spid")}</span>` },
  status: { get: (r) => val(r, "st") },
  database: { get: (r) => val(r, "db") },
  login: { get: (r) => ref(val(r, "ref")).login, html: (r) => esc(ref(val(r, "ref")).login) },
  host: { get: (r) => ref(val(r, "ref")).host, html: (r) => esc(ref(val(r, "ref")).host) },
  program: { get: (r) => ref(val(r, "ref")).prg, html: (r) => esc(ref(val(r, "ref")).prg) },
  command: { get: (r) => val(r, "cmd") },
  wait_type: { get: (r) => val(r, "w"), html: (r) => waitBadge(val(r, "w")) },
  wait_ms: { num: true, get: (r) => val(r, "wms"), html: (r) => `<span class="num">${n0(val(r, "wms"))}</span>` },
  elapsed: { num: true, get: (r) => val(r, "el"), html: (r) => `<span class="num">${n0(val(r, "el"))}</span>` },
  cpu_ms: { num: true, get: (r) => val(r, "cpu"), html: (r) => `<span class="num">${n0(val(r, "cpu"))}</span>` },
  logical_reads: { num: true, get: (r) => val(r, "rd"), html: (r) => `<span class="num">${n0(val(r, "rd"))}</span>` },
  writes: { num: true, get: (r) => val(r, "wr"), html: (r) => `<span class="num">${n0(val(r, "wr"))}</span>` },
  tempdb_mb: { num: true, get: (r) => val(r, "tdb"), html: (r) => (hasCap("tempdbPerTask") ? `<span class="num">${n2(val(r, "tdb"))}</span>` : NA) },
  memory_grant_mb: { num: true, get: (r) => val(r, "gr"), html: (r) => `<span class="num">${n2(val(r, "gr"))}</span>` },
  dop: { num: true, get: (r) => val(r, "dop"), html: (r) => (hasCap("requestDOP") ? `<span class="num">${val(r, "dop")}</span>` : NA) },
  // Blank rather than 0 % where the engine reports nothing, which is
  // everything but BACKUP, DBCC and a few others.
  percent_complete: { num: true, get: (r) => val(r, "pct"), html: (r) => (val(r, "pct") ? `<span class="num">${fPct1(val(r, "pct"))}</span>` : "") },
  blocked_by: { num: true, get: (r) => val(r, "by"), html: (r) => (val(r, "by") ? `<span class="blocked">${val(r, "by")}</span>` : "") },
  blocking_depth: { num: true, get: (r) => val(r, "d"), html: (r) => `<span class="num">${val(r, "d")}</span>` },
  sql_text: { get: (r) => ref(val(r, "ref")).sql, html: (r) => `<span class="sqlcell" style="padding-left:${val(r, "d") * 14}px">${esc(ref(val(r, "ref")).sql)}</span>` },
};

// The three views that are not the grid render as plain tables of tens of
// rows at human pace, so their cells produce text rather than markup: a
// textContent write escapes by construction, which is the right default for
// a database name, a program name or an object name coming off a server
// nobody here controls.
const CELL_SESSIONS = {
  spid: { num: true, text: (r) => n0(r.spid) },
  login: { text: (r) => r.login },
  host: { text: (r) => r.host },
  program: { text: (r) => r.program },
  status: { text: (r) => r.status },
  database: { text: (r) => r.database },
  connected: { num: true, text: (r) => fDur(r.connected) },
  // Blank while a request is running. The engine does report an end time
  // then, the previous request's, so the source is what decides this; here
  // a zero means "not idle" and drawing it as "0s" would read as one.
  idle: { num: true, text: (r) => (r.idle ? fDur(r.idle) : "") },
  open_tran: { num: true, text: (r) => n0(r.open_tran) },
  tran_age: { num: true, text: (r) => (r.tran_age ? fDur(r.tran_age) : "") },
  cpu_ms: { num: true, text: (r) => n0(r.cpu_ms) },
  logical_reads: { num: true, text: (r) => n0(r.logical_reads) },
  writes: { num: true, text: (r) => n0(r.writes) },
  memory_mb: { num: true, text: (r) => n2(r.memory_mb) },
};

const CELL_TRANSACTIONS = {
  xid: { num: true, text: (r) => n0(r.xid) },
  spid: { num: true, text: (r) => n0(r.spid) },
  name: { text: (r) => r.name },
  age: { num: true, text: (r) => fDur(r.age) },
  state: { text: (r) => r.state },
  type: { text: (r) => r.type },
  // A transaction spanning several databases says so rather than claiming
  // whichever one sorted first.
  database: { text: (r) => (r.databases > 1 ? r.database + " +" + (r.databases - 1) : r.database) },
  databases: { num: true, text: (r) => n0(r.databases) },
  log_mb: { num: true, text: (r) => n2(r.log_mb) },
  log_records: { num: true, text: (r) => n0(r.log_records) },
};

const CELL_LOCKS = {
  spid: { num: true, text: (r) => n0(r.spid) },
  database: { text: (r) => r.database },
  resource_type: { text: (r) => r.resource_type },
  // Empty means the name could not be resolved cheaply, not that there is
  // no object: only OBJECT locks carry one.
  object: { text: (r) => r.object },
  mode: { text: (r) => r.mode },
  status: { text: (r) => r.status },
  count: { num: true, text: (r) => n0(r.count) },
};

const CELL_LOGS = {
  database: { text: (r) => r.database },
  size_mb: { num: true, text: (r) => n2(r.size_mb) },
  used_mb: { num: true, text: (r) => n2(r.used_mb) },
  used_percent: { num: true, text: (r) => fPct1(r.used_percent) },
  reuse_wait: { text: (r) => r.reuse_wait },
  recovery_model: { text: (r) => r.recovery_model },
  state: { text: (r) => r.state },
};

// Which registry draws which view. Requests and blocking are the same rows
// read two ways, so they share one.
const CELLS = {
  requests: CELL_REQUESTS,
  blocking: CELL_REQUESTS,
  sessions: CELL_SESSIONS,
  transactions: CELL_TRANSACTIONS,
  locks: CELL_LOCKS,
  logs: CELL_LOGS,
};

// The columns actually drawn in the grid, in order. Built by applyColumns
// from what the server resolved out of the configuration file, so nothing
// here decides which columns exist or in what order they start.
let COLUMNS = [];

// One entry per view: the whole column list in saved order, hidden ones
// included because the order is what gets saved, plus each column's width
// and heading.
const layouts = new Map();
const viewKeys = new Map();
let activeView = "requests";

function waitBadge(w) {
  if (!w) return "";
  let cls = "";
  if (w.startsWith("LCK_")) cls = " lck";
  else if (w.startsWith("PAGEIOLATCH") || w === "WRITELOG" || w === "ASYNC_NETWORK_IO") cls = " io";
  else if (w === "CXPACKET") cls = " cx";
  return `<span class="badge${cls}">${esc(w)}</span>`;
}

// The fallback reads through col.get, not through the field name. Those are
// two different vocabularies now: field is the readable name the file and
// the catalogue use, and only get knows the terse name the same value has on
// the wire. Looking the field name up in the wire header instead rendered
// every column without an explicit html as empty, which is how status, the
// database and the command silently went blank.
function cellHTML(col, row) {
  return col.html ? col.html(row) : esc(col.get(row) ?? "");
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
  view = applyView(activeView === "blocking" ? blockingRows(data) : data);
  if (keepSelection) anchor();
  layout();
  renderDetail();
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
// stretchOf names the column that absorbs whatever the window has left
// over: the widest one, which is the one with something to say. Without it
// the table, which is max-content but at least as wide as its container,
// hands the surplus out in equal shares, and on a wide screen every column
// under about 180 px comes out at exactly the same width whatever its floor
// says. Measured in the browser, which is how that was found: eighteen
// columns, seventeen of them 178 px.
function stretchOf(cols) {
  let best = -1, w = -1;
  cols.forEach((c, i) => { if (c.width > w) { w = c.width; best = i; } });
  return best;
}

function head() {
  const stretch = stretchOf(COLUMNS);
  $("gridHead").innerHTML = COLUMNS.map((c, i) =>
    `<th scope="col" draggable="true" data-f="${esc(c.field)}" style="min-width:${c.width}px${i === stretch ? ";width:100%" : ""}">` +
    `<button type="button" class="sortBtn" data-f="${esc(c.field)}" ` +
    `title="sort by ${esc(c.title)}, drag to move">${esc(c.title)}<span class="sortMark" id="sort-${esc(c.field)}"></span></button></th>`).join("");

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
  // Dragging a heading moves the column. The gesture is on the <th> rather
  // than on the sort button inside it, so a click still sorts.
  for (const th of $("gridHead").children) {
    th.addEventListener("dragstart", (e) => {
      dragField = th.dataset.f;
      e.dataTransfer.effectAllowed = "move";
      // Firefox starts no drag at all without a payload, whatever the
      // effect says.
      e.dataTransfer.setData("text/plain", dragField);
    });
    th.addEventListener("dragover", (e) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      th.classList.add("dropTarget");
    });
    th.addEventListener("dragleave", () => th.classList.remove("dropTarget"));
    th.addEventListener("drop", (e) => {
      e.preventDefault();
      th.classList.remove("dropTarget");
      moveColumn(dragField, th.dataset.f);
      dragField = null;
    });
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

// One checkbox per column of the view, hidden ones included, in the order
// the file gave. Built once per connection: reordering by drag deliberately
// does not reshuffle it, because a list that moves under the pointer while
// you are ticking boxes is worse than one that does not match the header.
function buildColumnPanel() {
  const L = layouts.get(activeView);
  const cells = CELLS[activeView] || {};
  const list = $("colList");
  $("colWhich").textContent = activeView;
  list.innerHTML = (L ? L.order : []).filter((f) => cells[f]).map((f) =>
    `<label class="colItem"><input type="checkbox" data-f="${esc(f)}"${L.shown.has(f) ? " checked" : ""}>` +
    `<span>${esc(L.title.get(f) || f)}</span></label>`).join("");
  for (const el of list.querySelectorAll("input")) {
    el.addEventListener("change", () => {
      if (el.checked) L.shown.add(el.dataset.f);
      else L.shown.delete(el.dataset.f);
      applyColumns();
    });
  }
}

// One tab per view the server gave a key to. The list that lives inside
// another view, the locks under transactions, has none and gets no tab.
function buildTabs(views) {
  // The commands ride at the end of the same row as the view keys, so
  // "what can I press" has one place to look rather than being knowable
  // only by pressing h first. Built from COMMANDS, like the help dialog, so
  // the strip cannot advertise a key nothing is bound to.
  $("tabs").innerHTML = views.filter((v) => v.k).map((v) =>
    `<button type="button" data-v="${esc(v.id)}" id="tab-${esc(v.id)}">${esc(v.t)}` +
    `<span class="tabKey">${esc(v.k)}</span></button>`).join("") +
    `<span class="cmdHints">` + COMMANDS.filter(([, , short]) => short).map(([k, , short]) =>
      `<span><kbd>${esc(k)}</kbd>${esc(short)}</span>`).join("") + `</span>`;
  for (const b of $("tabs").querySelectorAll("button")) {
    b.addEventListener("click", () => setView(b.dataset.v));
  }
  markTabs();
}

// The commands of spec section 7, in one list: the help dialog is generated
// from it and the key handler dispatches from it, so a command cannot exist
// in one and not the other. app_commands_test.go checks that this list and
// the handler's own table cover each other.
// Key, what the help dialog says, and the one word the strip beside the
// tabs shows. Three columns rather than two because a status strip has room
// for a word and the dialog has room for a sentence.
// Key, what the help says, the word the strip beside the tabs shows, and a
// label for the help's key column when the key's name is not what you press.
// An empty word keeps a command out of the strip, which has room for the
// things you reach for and not for everything.
const COMMANDS = [
  ["t", "show the selected row's statement under the grid", "text"],
  ["s", "save the current state to snapshots/ beside the binary", "save"],
  ["p", "pause and resume the display", "pause"],
  ["f", "cycle the refresh period", "rate"],
  ["h", "this list", "help"],
  ["ArrowUp", "move the selection up the grid", "", "\u2191"],
  ["ArrowDown", "move the selection down the grid", "", "\u2193"],
];

function buildHelp() {
  $("helpList").innerHTML = COMMANDS.map(([k, what, , label]) =>
    `<dt>${esc(label || k)}</dt><dd>${esc(what)}</dd>`).join("");
  // The views come from the server, so the help cannot claim a tab that is
  // not there or miss one that is.
  $("helpViews").innerHTML = [...viewKeys].map(([k, id]) =>
    `<dt>${esc(k)}</dt><dd>${esc((layouts.get(id) && id) || id)}</dd>`).join("");
}
//
// setup-region: end

// setGrid takes every view and its column selection, as the server
// resolved them from the configuration file. Sent once per connection.
function setGrid(views) {
  viewKeys.clear();
  for (const v of views) {
    // A view already laid out keeps what it has. This runs again on every
    // reconnection, and the file cannot change under a running process
    // except through this page's own save, which the page already reflects;
    // rebuilding from the server would throw away a column the user had
    // just moved and not yet saved.
    if (layouts.has(v.id)) {
      if (v.k) viewKeys.set(v.k, v.id);
      continue;
    }
    const L = { order: [], shown: new Set(), width: new Map(), title: new Map() };
    for (const c of v.cols || []) {
      L.order.push(c.f);
      L.width.set(c.f, c.w);
      L.title.set(c.f, c.t);
      if (c.s) L.shown.add(c.f);
    }
    layouts.set(v.id, L);
    if (v.k) viewKeys.set(v.k, v.id);
  }
  if (!layouts.has(activeView)) activeView = views.length ? views[0].id : "requests";
  buildTabs(views);
  buildHelp();
  buildColumnPanel();
  applyColumns();
}

// columnsFor joins one view's saved layout with the registry that knows how
// to draw its cells. A column the registry cannot draw is dropped rather
// than rendered blank; the catalogue test makes that case impossible in a
// shipped build, and this keeps a mismatched pair from breaking the page.
function columnsFor(view) {
  const L = layouts.get(view);
  const cells = CELLS[view];
  if (!L || !cells) return [];
  return L.order
    .filter((f) => L.shown.has(f) && cells[f])
    .map((f) => Object.assign({ field: f, title: L.title.get(f) || f, width: L.width.get(f) || 100 }, cells[f]));
}

// applyColumns rebuilds the active view. For the grid that means the header
// and the row pool: the pool has one cell per column, so a change of column
// count is the one thing the per-cell update path cannot absorb and has to
// throw the pool away.
function applyColumns() {
  if (!isGrid(activeView)) {
    renderActiveList();
    return;
  }
  const L = layouts.get(activeView);
  COLUMNS = columnsFor(activeView);

  // A filter or a sort on a column that has just been hidden would go on
  // shaping the grid with nothing on screen to say so.
  const dropped = [...filters.keys()].filter((f) => !L.shown.has(f));
  for (const f of dropped) filters.delete(f);
  if (sortField && !L.shown.has(sortField)) {
    sortField = null;
    sortDir = 0;
  }

  const body = $("gridBody");
  body.textContent = "";
  pool.length = 0;
  spacerTop = null;
  spacerBottom = null;

  head();
  markSort();
  // head() built empty boxes; put back what was typed in the ones that
  // survived, through setFilter so nothing can drift apart.
  for (const [field, text] of [...filters]) {
    const el = $("f-" + field);
    if (el) setFilter(el, text);
  }
  refresh();
}

const isGrid = (v) => v === "requests" || v === "blocking";

function markTabs() {
  for (const b of $("tabs").querySelectorAll("button")) b.classList.toggle("on", b.dataset.v === activeView);
}

// setView switches tab. The three list views are filled on demand and left
// alone otherwise, so their queries only run while somebody is reading the
// answer; the grid needs no request, being a projection of the retention
// window the stream already delivers.
function setView(id) {
  if (!layouts.has(id) || id === activeView) return;
  activeView = id;
  markTabs();
  document.querySelector(".gridScroll").hidden = !isGrid(id);
  $("detail").hidden = !isGrid(id) || !detailOpen;
  for (const v of ["sessions", "transactions", "logs"]) $("panel-" + v).hidden = v !== id;
  buildColumnPanel();
  applyColumns();
  if (!isGrid(id)) pollView();
}

// blockingRows keeps the chains and drops everything else. The rows arrive
// already flattened, a blocker immediately above what it blocks, so this
// only has to decide membership and never reorders.
function blockingRows(rows) {
  const blockers = new Set();
  for (const r of rows) {
    const by = val(r, "by");
    if (by) blockers.add(by);
  }
  return rows.filter((r) => val(r, "by") || blockers.has(val(r, "spid")));
}

// The shortest gap between two asks of each list view, in milliseconds.
// Measured rather than guessed: docs/PERFORMANCE.md records 0.1 ms of
// server CPU per call for the sessions query, 1.5 ms for the log list and
// tens of milliseconds for the locks, against an observation budget of 50 ms
// per second. Only the lock scan grows with the server, and polling that at
// the grid's one second would spend most of the tool's allowance on one tab;
// at five seconds it is about a sixth of it, which is what an operator
// watching a lock list is asking for.
const POLL_FLOOR = { sessions: 2000, transactions: 5000, logs: 5000 };

// pollView asks for the active list view and schedules the next ask, at
// whichever is slower, the sampling period or that view's own floor.
let pollTimer = 0;
function pollView() {
  clearTimeout(pollTimer);
  const v = activeView;
  if (isGrid(v)) return;
  fetch("/api/" + v + "?t=" + encodeURIComponent(token))
    .then((r) => r.json().then((j) => (r.ok ? j : Promise.reject(new Error(j.error || r.statusText)))))
    // Guarded because a response can land after the user has moved on: it
    // would then redraw a hidden panel with the view's own data, which is
    // harmless, and set that view's cached payload to something older than
    // what a later request may already have returned, which is not.
    .then((j) => { if (activeView === v) renderList(v, j); })
    .catch((e) => showListError(v, e.message))
    .finally(() => {
      if (activeView === v && !paused) pollTimer = setTimeout(pollView, Math.max(periodMs || 1000, POLL_FLOOR[v] || 5000));
    });
}

// The last payload each list view received, so switching a column on or
// off redraws immediately instead of waiting for the next poll.
const lastList = {};
function renderActiveList() {
  if (!isGrid(activeView) && lastList[activeView]) renderList(activeView, lastList[activeView]);
}

// renderList builds nodes rather than markup. These tables hold tens of
// rows and refresh at human pace, so there is nothing to win from the
// grid's per-cell diffing, and textContent escapes by construction.
function renderList(view, payload) {
  lastList[view] = payload;
  const panel = $("panel-" + view);
  panel.textContent = "";
  panel.appendChild(listTable(view, payload.rows || []));
  if (view === "transactions") {
    const h = document.createElement("h2");
    h.className = "listHeading";
    h.textContent = "locks held by those transactions";
    panel.appendChild(h);
    panel.appendChild(listTable("locks", payload.locks || []));
  }
}

function listTable(view, rows) {
  const cols = columnsFor(view);
  const t = document.createElement("table");
  t.className = "listTable";
  const hr = t.createTHead().insertRow();
  const stretch = stretchOf(cols);
  cols.forEach((c, i) => {
    const th = document.createElement("th");
    th.scope = "col";
    th.textContent = c.title;
    th.style.minWidth = c.width + "px";
    if (i === stretch) th.style.width = "100%";
    hr.appendChild(th);
  });
  const tb = t.createTBody();
  for (const r of rows) {
    const tr = tb.insertRow();
    for (const c of cols) {
      const td = tr.insertCell();
      td.textContent = c.text(r);
      // numCell, not the grid's num. That one carries display: block,
      // because in the grid it styles a span inside a cell; put on the cell
      // itself it stops the cell being a table-cell and drops it out of its
      // own row, which is what every numeric column of these three views
      // did until a screenshot showed it.
      if (c.num) td.className = "numCell";
    }
  }
  if (!rows.length) {
    const td = tb.insertRow().insertCell();
    td.colSpan = Math.max(1, cols.length);
    td.className = "empty";
    td.textContent = "nothing to show";
  }
  return t;
}

function showListError(view, message) {
  const panel = $("panel-" + view);
  panel.textContent = "";
  const p = document.createElement("p");
  p.className = "listError";
  p.textContent = message;
  panel.appendChild(p);
}

let dragField = null;

// moveColumn drops from in front of to, in the full order rather than the
// visible one, so a hidden column between them keeps its place.
function moveColumn(from, to) {
  if (!from || !to || from === to) return;
  const order = layouts.get(activeView).order;
  const i = order.indexOf(from);
  const j = order.indexOf(to);
  if (i < 0 || j < 0) return;
  order.splice(i, 1);
  order.splice(order.indexOf(to) + (i < j ? 1 : 0), 0, from);
  applyColumns();
}

// post is the one place this page writes to the server. Every endpoint
// answers JSON and refuses anything but POST, so a failure is a body worth
// showing rather than a status code to guess from.
function post(path, body, type) {
  return fetch(path + "?t=" + encodeURIComponent(token), {
    method: "POST",
    headers: { "Content-Type": type },
    body: body,
  }).then((r) => (r.ok ? r.json() : r.text().then((t) => Promise.reject(new Error(t.trim())))));
}

// Command feedback goes to its own element. It used to share #message with
// the collector, which rewrites that line on every tick from the server's
// own status, so "snapshot written to ..." survived for one tick at most and
// usually for none.
let noticeTimer = 0;
function showRate() {
  $("rate").textContent = periodMs ? "every " + (periodMs >= 1000 ? periodMs / 1000 + " s" : periodMs + " ms") : "";
}

function say(text) {
  $("notice").textContent = text;
  clearTimeout(noticeTimer);
  if (text) noticeTimer = setTimeout(() => { $("notice").textContent = ""; }, 8000);
}

// saveLayout writes the current selection and order back to sqltop.yaml,
// through the server, which owns that file. Spec section 8.2: a layout
// survives a change of browser and can be handed to a colleague, which
// local storage cannot do.
function saveLayout() {
  const L = layouts.get(activeView);
  const columns = L.order.map((f) => ({ field: f, show: L.shown.has(f), width: L.width.get(f) || 0 }));
  post("/api/layout", JSON.stringify({ view: activeView, columns }), "application/json")
    .then((r) => say("layout saved to " + r.path))
    .catch((e) => say("could not save the layout: " + e.message));
}

// Paused freezes what is on screen. The stream is left running and its
// references are still absorbed, so resuming does not find rows whose SQL
// text was sent while nobody was listening: the server sends a reference
// once and never again while the session is alive.
let paused = false;
function togglePause() {
  paused = !paused;
  $("pauseMark").hidden = !paused;
  if (paused) {
    say("display paused, press p to resume");
    return;
  }
  say("");
  // pollView stops rescheduling itself while paused, so resuming has to
  // start it again. Without this a paused list view stayed frozen after
  // resuming until the user switched tabs and back.
  if (!isGrid(activeView)) pollView();
}

// The ladder the f command steps through. Fixed rather than typed in:
// pressing one key is the whole point, and these are the periods anybody
// actually wants.
const RATES = [1000, 2000, 5000, 10000, 30000];
let rateIdx = -1;
let periodMs = 0;

// cycleFrequency changes how often the tool samples the server, not how
// often the page redraws: the sampling rate is what costs the monitored
// instance anything, and slowing down on a struggling server is the whole
// reason to have this key.
function cycleFrequency() {
  if (rateIdx < 0) {
    rateIdx = RATES.findIndex((r) => r > periodMs);
    if (rateIdx < 0) rateIdx = 0;
  } else {
    rateIdx = (rateIdx + 1) % RATES.length;
  }
  post("/api/period", JSON.stringify({ period: RATES[rateIdx] + "ms" }), "application/json")
    .then((r) => {
      // Shown from the answer rather than waited for. The status carries
      // the period too, but it arrives on the next tick, which is now the
      // new and slower one: stepping to thirty seconds meant the footer
      // still read the old rate for half a minute, and the list views went
      // on polling at the old cadence for one more cycle. The next status
      // corrects this if the throttle has doubled it.
      periodMs = RATES[rateIdx];
      showRate();
      say("sampling every " + r.period);
    })
    .catch((e) => say("could not change the refresh period: " + e.message));
}

// snapshotHTML writes the state out as one standalone document. The grid is
// virtualised, so the live DOM holds about forty rows of however many the
// view has; saving the document would save the scroll position rather than
// the state, hence the full table below. The header, the identity row and
// the dashboard are taken from the page as they stand, which is what makes
// the file look like what was on screen.
function snapshotHTML() {
  const stamp = new Date().toISOString().replace("T", " ").slice(0, 19);
  const active = [...filters].map(([f, t]) => f + " " + t).join(", ") || "none";
  const sorted = sortField ? sortField + (sortDir === 1 ? " ascending" : " descending") : "none";
  return '<!DOCTYPE html>\n<html lang="en"><head><meta charset="utf-8">' +
    `<title>sqltop ${esc($("instance").textContent)} ${esc(stamp)}</title>` +
    `<style>${document.querySelector("style").textContent}` +
    // A snapshot is a document, not an application: it scrolls with the
    // page instead of inside a pane sized to a window that is not there.
    "body{height:auto;display:block}#grid{width:100%}</style></head><body>" +
    `<header><h1>sqltop</h1><div class="conn"><span>${esc($("instance").textContent)}</span></div>` +
    `<div class="build">${esc($("build").textContent)}</div></header>` +
    $("serverInfo").outerHTML +
    `<p class="message">snapshot ${esc(stamp)}, filters: ${esc(active)}, sort: ${esc(sorted)}, ` +
    `${view.length} of ${data.length} requests</p>` +
    `<div class="dashBody">${$("dashBody").innerHTML}</div>` +
    `<table id="grid"><thead><tr>${COLUMNS.map((c) => `<th>${esc(c.title)}</th>`).join("")}</tr></thead><tbody>` +
    view.map((r) => "<tr>" + COLUMNS.map((c) => `<td>${cellHTML(c, r)}</td>`).join("") + "</tr>").join("") +
    "</tbody></table></body></html>";
}

function saveSnapshot() {
  post("/api/snapshot", snapshotHTML(), "text/html")
    .then((r) => say("snapshot written to " + r.path))
    .catch((e) => say("could not write the snapshot: " + e.message));
}

// A T-SQL tokeniser rather than a highlighting library. This page carries no
// dependencies and is served inline in full on every load; the smallest such
// library is larger than everything else here put together, and this has six
// things to tell apart.
//
// The list is for readability, not for correctness: a keyword missing from
// it renders as plain text, which is what a name renders as anyway.
const SQL_KEYWORDS = new Set((
  "select from where group by having order asc desc into values set output " +
  "insert update delete merge truncate " +
  "join inner left right full outer cross apply on " +
  "union all except intersect distinct top offset fetch next rows only percent " +
  "with as case when then else end " +
  "and or not in exists between like is null " +
  "declare begin commit rollback transaction tran save " +
  "create alter drop table view index procedure function trigger schema database " +
  "if while return break continue goto exec execute " +
  "over partition row_number rank dense_rank ntile " +
  "cast convert try_cast try_convert count sum avg min max " +
  "isnull coalesce nullif datediff dateadd getdate sysdatetime " +
  "use option maxdop recompile nolock readuncommitted rowlock updlock holdlock readpast " +
  "primary key foreign references constraint default identity unique check " +
  "int bigint smallint tinyint bit decimal numeric float real money " +
  "char varchar nchar nvarchar text date datetime datetime2 time uniqueidentifier"
).split(" "));

// One pass, one regular expression. The string and comment forms are written
// so they cannot backtrack: '[^']*(?:''[^']*)*' rather than '(?:[^']|'')*',
// which is quadratic on a long literal.
const SQL_TOKEN = /--[^\n]*|\/\*[\s\S]*?\*\/|'[^']*(?:''[^']*)*'|"[^"]*(?:""[^"]*)*"|\[[^\]]*\]|@@?[A-Za-z_]\w*|\d+(?:\.\d+)?|[A-Za-z_]\w*/g;

// sqlNodes builds nodes, never markup: textContent escapes by construction,
// which is the right default for a statement that came off a server nobody
// here controls, and it keeps this out of the render-path guard that stops
// the grid rebuilding rows through innerHTML.
function sqlNodes(sql) {
  const frag = document.createDocumentFragment();
  let last = 0;
  for (const m of sql.matchAll(SQL_TOKEN)) {
    if (m.index > last) frag.appendChild(document.createTextNode(sql.slice(last, m.index)));
    const t = m[0];
    const c = t[0];
    let cls = "";
    if (c === "-" || c === "/") cls = "c";
    else if (c === "'" || c === '"') cls = "s";
    else if (c === "[") cls = "i";
    else if (c === "@") cls = "v";
    else if (c >= "0" && c <= "9") cls = "n";
    else if (SQL_KEYWORDS.has(t.toLowerCase())) cls = "k";
    if (cls) {
      const el = document.createElement("span");
      el.className = cls;
      el.textContent = t;
      frag.appendChild(el);
    } else {
      frag.appendChild(document.createTextNode(t));
    }
    last = m.index + t.length;
  }
  if (last < sql.length) frag.appendChild(document.createTextNode(sql.slice(last)));
  return frag;
}

// The statement panel. The text is already here: the server sends a
// session's SQL once and the reference table holds it, so opening this
// costs no request and no query.
let detailOpen = false;
let detailShown = null;

function toggleDetail() {
  if (!isGrid(activeView)) {
    say("the statement panel follows the requests and blocking views");
    return;
  }
  detailOpen = !detailOpen;
  $("detail").hidden = !detailOpen;
  detailShown = null;
  renderDetail();
  // The grid gives up its height to the panel, so it has to be told.
  layout();
}

// renderDetail runs on every tick while the panel is open, and does nothing
// on the ticks where the statement has not changed. It has to run on ticks
// at all because a session moves from one statement to the next without the
// selection changing, and a panel still showing the previous one would be
// the same lie as a stale figure on a tile.
function renderDetail() {
  if (!detailOpen) return;
  const r = selectedKey === null ? null : view.find((x) => rowKey(x) === selectedKey);
  const sql = r ? ref(val(r, "ref")).sql : "";
  const label = r
    ? "spid " + val(r, "spid") + "  " + (val(r, "db") || "") + "  " + n0(val(r, "el")) + " ms"
    : "select a row to see its statement";
  if ($("detailWho").textContent !== label) $("detailWho").textContent = label;
  if (detailShown === sql) return;
  detailShown = sql;
  const pre = $("sqlText");
  pre.textContent = "";
  if (sql) pre.appendChild(sqlNodes(sql));
}

// moveSelection walks the grid one row at a time. The grid is virtualised,
// so the row it moves to may not be in the DOM: the selection is an index
// into view, the scroll follows it, and layout draws whatever that lands on.
// Nothing is selected to begin with, so the first press takes the end of the
// list it came from.
function moveSelection(delta) {
  if (!isGrid(activeView) || view.length === 0) return;
  let i = selectedKey === null ? -1 : view.findIndex((r) => rowKey(r) === selectedKey);
  if (i < 0) i = delta > 0 ? 0 : view.length - 1;
  else i = Math.max(0, Math.min(view.length - 1, i + delta));
  selectedKey = rowKey(view[i]);

  // Scrolled to only when it would otherwise be off screen, so holding a key
  // down walks the list rather than dragging the viewport a row at a time.
  //
  // The heading rows count twice over. They sit in the flow above the body,
  // so scrollTop is measured past them, and they are sticky, so they cover
  // the top of what is under them. A row is therefore fully in view between
  // scrollTop + headH and scrollTop + clientHeight, and forgetting the first
  // half leaves the last row of a downward walk fourteen pixels below the
  // pane, which is what the browser test measured.
  const sc = document.querySelector(".gridScroll");
  const head = document.querySelector("#grid thead");
  const headH = head ? head.offsetHeight : 0;
  const top = i * ROW_H;
  if (top < sc.scrollTop) sc.scrollTop = top;
  else if (headH + top + ROW_H > sc.scrollTop + sc.clientHeight) sc.scrollTop = headH + top + ROW_H - sc.clientHeight;

  // layout marks the selected row itself, so there is nothing to toggle here.
  layout();
  renderDetail();
}

function toggleHelp() {
  const d = $("helpDialog");
  if (d.open) d.close();
  else d.showModal();
}

// One entry per command, keyed the way the keyboard reports it. The keys
// here and the keys in COMMANDS are checked against each other by a test.
const KEYS = {
  t: toggleDetail,
  s: saveSnapshot,
  p: togglePause,
  f: cycleFrequency,
  h: toggleHelp,
  ArrowUp: () => moveSelection(-1),
  ArrowDown: () => moveSelection(1),
};

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
    renderDetail();
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
  // The rate the tool is actually sampling at, which is not always the one
  // that was asked for: the budget halves it when the tool costs the server
  // too much, and a status bar that showed the request would be wrong
  // exactly when it mattered.
  periodMs = st.periodMs || 0;
  showRate();
}

const es = new EventSource("/api/stream?t=" + encodeURIComponent(token));
es.addEventListener("snapshot", (e) => {
  const p = JSON.parse(e.data);
  // The column header and the dashboard shape both come once, on the first
  // snapshot of a connection.
  if (p.cols) setCols(p.cols);
  if (p.grid) setGrid(p.grid);
  if (p.dash) buildDashboard(p.dash);
  if (paused) {
    // Absorbed even while frozen: a reference is sent once for the life of
    // a session, so dropping one here would leave a row permanently
    // without its SQL text after resuming.
    if (p.refs) for (const [k, v] of Object.entries(p.refs)) refs.set(k, v);
    return;
  }
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
$("colsBtn").addEventListener("click", () => $("colDialog").showModal());
$("colSave").addEventListener("click", saveLayout);
$("helpBtn").addEventListener("click", toggleHelp);
buildHelp();
// setGrid calls it again once the server has said which views exist, so
// the list starts with the four commands and gains the tabs on connecting.

// Single keypresses, in the spirit of top and of the PowerShell prototype.
// Ignored while the focus is in a filter box, or the letters would be
// commands instead of text; modifiers are left to the browser.
globalThis.addEventListener("keydown", (e) => {
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  const t = e.target;
  if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
  const fn = KEYS[e.key];
  const view = viewKeys.get(e.key);
  if (!fn && !view) return;
  e.preventDefault();
  if (fn) fn();
  else setView(view);
});
// Counts locally: it keeps moving while the connection is down, which is
// honest, the instance is still up and this tool just cannot see it.
setInterval(updateUptime, 1000);
