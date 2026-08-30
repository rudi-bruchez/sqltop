// The renderer the bench settled on: a pool of recycled <tr> covering the
// visible window, two spacers holding the scroll height, and only changed
// cells rewritten. Measured at 4.8 ms per refresh over 800 rows against 46.8
// for Tabulator, with no freeze and no lost selection. See docs/SPECS.md
// section 10.1 for the measurements behind that choice.
"use strict";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const n0 = (v) => Math.round(v).toLocaleString("en-US");
const n2 = (v) => Number(v).toFixed(2);
const NA = '<span class="num na">n/a</span>';

// Dashboard formatters. Each one takes the raw number a model.Figure
// carries and returns what the tile shows; none of them is ever called for
// an unavailable figure, so none has to invent a reading.
const fInt = (v) => n0(v);
const fNum1 = (v) => Number(v).toFixed(1);
const fNum2 = (v) => Number(v).toFixed(2);
const fPct = (v) => Math.round(v) + " %";
const fPct1 = (v) => Number(v).toFixed(1) + " %";
const fMB = (v) => (Math.abs(v) >= 1024 ? (v / 1024).toFixed(1) + " GB" : Number(v).toFixed(1) + " MB");
const fKB = (v) => fMB(v / 1024);
// Rates lose their decimal once they are large enough for it to be noise.
const fRate = (v) => (Math.abs(v) >= 100 ? n0(v) + "/s" : Number(v).toFixed(1) + "/s");
// Signed, because a version store that is shrinking is the answer an
// operator waiting for a long snapshot reader to finish is looking for, and
// an unsigned 0.42 would read as still growing.
const fSigned = (v) => (v >= 0 ? "+" : "") + Number(v).toFixed(2) + " MB/s";

// fDur renders a count of seconds the way a person reads a duration. Used
// for page life expectancy, the longest running transaction and the
// instance uptime, all three of which span seconds to weeks.
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

// refs accumulates the per-session invariants the server sends once. A row
// only ever carries the key into this table (r.ref); the SQL text, program,
// login and host live here so they are never repeated on the wire.
const refs = new Map();
function ref(key) {
  return refs.get(key) || { sql: "", prg: "", login: "", host: "" };
}

// rowKey identifies one grid row across ticks. It must fold in the request
// id, not just the session id: under MARS one session can run several
// concurrent requests (internal/model.RequestRef), and keying recycled rows
// on the session id alone would let one request's row silently take over
// another's, dropping the selection or the scroll position exactly the way
// the bench measured for the renderers it rejected.
function rowKey(r) { return r.spid + ":" + r.rqid; }

// caps holds the capability names the connected source can actually
// provide (StatusPayload.Caps, internal/web/protocol.go). A source below
// SQL Server 2016 has no dop column, and a login without the tempdb DMV
// right has no tempdb figure; without this, buildRequestsQuery on the Go
// side substitutes a literal 0 for either and the grid would assert that
// as a measurement to someone diagnosing an incident (fix round 1, task
// 14). hasCap greys the column instead.
let caps = new Set();
function hasCap(name) { return caps.has(name); }

// FIG_GROUPS is spec section 6's dashboard, in the order an operator reads
// a server that is misbehaving: what the CPU is doing, then what memory is
// doing, then how much work is arriving, then what is holding on to things.
//
// Every key here is a key in SnapshotPayload.Figures. A key this list names
// and the server never sends renders exactly like one the server sends with
// available false: no number. That is deliberate. The distinction between
// "this build does not collect it" and "this server cannot answer it"
// matters to whoever is changing the code and not at all to whoever is
// looking at a server at three in the morning, and inventing a third visual
// state for it would only make the page harder to read.
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
      // Read windowed, not raw. Spec section 6: the raw counter sits at
      // 99-point-something on every server forever and says nothing, so
      // the server differentiates it against its base counter and the
      // figure below is the reading for the last interval. Page life
      // expectancy sits next to it because it is the one to trust.
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

// HISTORY_MAX is how many ticks of history a sparkline keeps. Spec section
// 6 asks for a sparkline "over the retention window", and this is not that:
// the retention window is fifteen minutes, which at one tick a second is
// nine hundred points, and a sparkline a hundred pixels wide cannot draw
// nine hundred points as anything but a smear. So it keeps what it can
// actually render, and the panel heading states the span it is showing
// rather than letting the reader assume it matches the grid's window. The
// way out, when someone wants the full window, is a real chart in a view of
// its own (spec section 7), not a wider tile.
const HISTORY_MAX = 120;

// history holds one series per figure key: parallel arrays of the tick
// number a reading came from and the reading itself. Unavailable ticks
// append nothing, so a figure that drops out for a while draws one long
// straight segment across the gap rather than silently compressing its own
// time axis, and every sparkline on the page shares one x scale so two
// tiles side by side can be compared.
const history = new Map();
const tiles = new Map();
// span tracks wall clock against tick numbers, purely so the panel heading
// can say how much time it is showing. The tick period is not fixed: the
// observation budget slows the collector down under load, so a hundred and
// twenty ticks is two minutes on a healthy server and rather more on one
// that made the tool throttle itself.
const span = [];

const COLUMNS = [
  { field: "spid", title: "spid", width: 60, html: (r) => `<span class="num">${r.spid}</span>` },
  { field: "st", title: "status", width: 90 },
  { field: "db", title: "database", width: 110 },
  { field: "login", title: "login", width: 100, html: (r) => esc(ref(r.ref).login) },
  { field: "host", title: "host", width: 95, html: (r) => esc(ref(r.ref).host) },
  { field: "prg", title: "program", width: 200, html: (r) => esc(ref(r.ref).prg) },
  { field: "cmd", title: "command", width: 110 },
  { field: "w", title: "wait type", width: 150, html: (r) => waitBadge(r.w) },
  { field: "wms", title: "wait ms", width: 85, html: (r) => `<span class="num">${n0(r.wms)}</span>` },
  { field: "el", title: "elapsed ms", width: 100, html: (r) => `<span class="num">${n0(r.el)}</span>` },
  { field: "cpu", title: "cpu ms", width: 90, html: (r) => `<span class="num">${n0(r.cpu)}</span>` },
  { field: "rd", title: "reads", width: 95, html: (r) => `<span class="num">${n0(r.rd)}</span>` },
  { field: "wr", title: "writes", width: 90, html: (r) => `<span class="num">${n0(r.wr)}</span>` },
  { field: "tdb", title: "tempdb MB", width: 100, html: (r) => (hasCap("tempdbPerTask") ? `<span class="num">${n2(r.tdb)}</span>` : NA) },
  { field: "gr", title: "grant MB", width: 95, html: (r) => `<span class="num">${n2(r.gr)}</span>` },
  { field: "dop", title: "dop", width: 55, html: (r) => (hasCap("requestDOP") ? `<span class="num">${r.dop}</span>` : NA) },
  { field: "by", title: "blocked by", width: 95, html: (r) => (r.by ? `<span class="blocked">${r.by}</span>` : "") },
  { field: "sql", title: "SQL text", width: 520, html: (r) => `<span class="sqlcell" style="padding-left:${r.d * 14}px">${esc(ref(r.ref).sql)}</span>` },
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
  return col.html ? col.html(row) : esc(row[col.field] ?? "");
}

const ROW_H = 22;   // must match the height fixed in style.css
const OVERSCAN = 8; // rows rendered off screen, for clean scrolling
let data = [];
let selectedKey = null;
const pool = []; // {tr, tds, key, prev}
let spacerTop = null, spacerBottom = null;

// setup-region: begin
//
// The two functions below are the only places this file writes innerHTML
// outside the per-cell rewrite layout() performs, and both are legitimate:
// building the header row and the dashboard tiles once, from fixed lists,
// is setup work, not the per-tick update path app_assets_test.go's
// regression guard is protecting. The guard reads the two marker comments
// around them, verbatim, to tell setup work apart from the render path,
// and it takes the last marker of each kind it finds, so there must be
// exactly one region: see that file before moving, renaming, splitting or
// removing either marker.
function head() {
  $("gridHead").innerHTML = COLUMNS.map((c) => `<th scope="col" style="min-width:${c.width}px">${esc(c.title)}</th>`).join("");
}

// buildDashboard lays the tiles out once and remembers the two nodes each
// one updates: the value text and the sparkline's polyline. After this runs
// the dashboard's per-tick path writes textContent and one attribute per
// tile and never touches markup again, which is the same discipline the
// grid is held to and for the same reason.
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

// sparkPoints turns one series into an SVG polyline. The y scale is the
// series' own minimum to maximum, not an absolute one: a sparkline exists
// to show a slope, and a cache hit ratio pinned to a 0-100 axis would be a
// flat line at the top of the tile on every server ever built. A series
// that genuinely does not move draws through the middle rather than along
// the floor, where it would read as zero.
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

// updateDashboard is the dashboard's whole render path. Like layout(), it
// writes only what changed, and it writes text and attributes, never
// markup.
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
    // An unavailable figure says so. It never falls back to the last value
    // it had, which would be the most convincing lie this page could tell:
    // a stale number that looks live is worse than no number, and the whole
    // Available flag exists to keep that from happening at any layer.
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

// layout is the whole render path. It never touches innerHTML on the table,
// the body, or a whole row: only on the one <td> whose formatted content
// actually changed. Rebuilding rows through innerHTML in here, in a helper
// it calls, or through outerHTML or insertAdjacentHTML anywhere in this
// path, is exactly the mistake app_assets_test.go's
// TestGridUpdatePathNeverWritesMarkupOutsideItsSetupRegion guards against,
// because it is invisible on review and would throw away the 4.8 ms figure
// this renderer exists for (docs/SPECS.md section 10.1).
function layout() {
  const sc = document.querySelector(".gridScroll");
  if (!sc) return;
  ensureSpacers();

  const total = data.length;
  const visible = Math.ceil(sc.clientHeight / ROW_H) + OVERSCAN * 2;
  let first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - OVERSCAN);
  // Clamp first to the last page the current row count actually has.
  // Without this, a row count that collapses while the user is scrolled
  // down (a storm of requests clearing to a handful, the ordinary case,
  // not an edge case) leaves first pointing past the end of data: count
  // computes to 0, nothing renders, and the grid looks empty until the
  // next manual scroll happens to recompute first from a still-elevated
  // scrollTop. This is the lost-scroll failure the renderer was chosen to
  // prevent, reached by a route the bench's own generator never exercised
  // because it held the row count constant (fix round 1, task 14).
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

// startedAt is the instance start time in Unix milliseconds, zero when the
// source could not read it. The uptime ticks locally off this instant
// rather than being recomputed from each snapshot, so it keeps counting at
// one second even when the observation budget has slowed the stream down.
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
  // connState carries the accessible connection state, not dot: a live
  // region (role="status"/aria-live) announces on a content change, not on
  // an accessible-name change, so toggling dot's aria-label here would
  // never actually be read out. dot stays a decorative, aria-hidden visual
  // cue; connState's textContent is what a screen reader hears (fix round
  // 1, task 14).
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
  // Spec section 10: "an instrument that claims to bound its own cost
  // should show it", at all times, not only once the tool has already
  // throttled itself - which is the only place this number used to reach
  // the browser, interpolated into the throttle message.
  $("cost").textContent = "cost: " + Math.round(st.costMsPerSecond || 0) + " ms/s";
}

const es = new EventSource("/api/stream?t=" + encodeURIComponent(token));
es.addEventListener("snapshot", (e) => {
  const p = JSON.parse(e.data);
  data = p.rows || [];
  caps = new Set((p.status && p.status.caps) || []);

  // Prune refs the same way the server's Encoder prunes its own sent set
  // (protocol.go's Snapshot): a key not used by any row this tick is
  // dropped. Without this the client kept every reference it had ever
  // seen for as long as the tab stayed open, unbounded (fix round 1, task
  // 14: measured at roughly 2.9 new entries per tick against a real
  // server, extrapolating to about 10,000 entries and 1.3 MB retained per
  // hour left open). If the same key returns later, the server has
  // forgotten it too by the same rule and will resend it.
  const alive = new Set(data.map((r) => r.ref));
  for (const k of refs.keys()) if (!alive.has(k)) refs.delete(k);
  if (p.refs) for (const [k, v] of Object.entries(p.refs)) refs.set(k, v);

  layout();
  applyStatus(p.status || {}, p.seq);

  // Active requests is the one dashboard figure the server does not send.
  // Spec section 6 lists it as "counted from the grid data, free", and it
  // genuinely is: the rows are already here, so putting it on the wire
  // would be paying for a number we can read off what just arrived. It is
  // injected into a copy of the figures rather than into p.figures so
  // nothing downstream can mistake it for something the collector measured.
  const figures = Object.assign({ active_requests: { value: data.length, unit: "", available: true } }, p.figures || {});
  updateDashboard(figures, p.seq, p.ts || Date.now());
});
es.addEventListener("error", () => {
  $("dot").classList.remove("live");
  $("connState").textContent = "disconnected";
  $("message").textContent = "lost the connection to sqltop, retrying";
});

document.querySelector(".gridScroll").addEventListener("scroll", layout, { passive: true });
// Without this, enlarging the window leaves the newly visible space empty
// until the next snapshot happens to call layout() on its own (fix round
// 1, task 14).
window.addEventListener("resize", layout);
head();
buildDashboard();
// The uptime counts locally rather than waiting for the next snapshot, so
// it moves at one second on a throttled stream and keeps moving while the
// connection is down, which is honest: the instance is still up, this tool
// just cannot see it.
setInterval(updateUptime, 1000);
