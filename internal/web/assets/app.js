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
// head() below is the one place this file writes innerHTML outside the
// per-cell rewrite layout() performs, and it is legitimate: building the
// header row once, from a fixed column list, is setup work, not the
// per-tick update path app_assets_test.go's regression guard is
// protecting. The guard reads the two marker comments around this
// function, verbatim, to tell setup work apart from the render path; see
// that file before moving, renaming or removing either marker.
function head() {
  $("gridHead").innerHTML = COLUMNS.map((c) => `<th scope="col" style="min-width:${c.width}px">${esc(c.title)}</th>`).join("");
}
//
// setup-region: end

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
