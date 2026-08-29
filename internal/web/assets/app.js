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
  { field: "tdb", title: "tempdb MB", width: 100, html: (r) => `<span class="num">${n2(r.tdb)}</span>` },
  { field: "gr", title: "grant MB", width: 95, html: (r) => `<span class="num">${n2(r.gr)}</span>` },
  { field: "dop", title: "dop", width: 55, html: (r) => `<span class="num">${r.dop}</span>` },
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

function head() {
  $("gridHead").innerHTML = COLUMNS.map((c) => `<th scope="col" style="min-width:${c.width}px">${esc(c.title)}</th>`).join("");
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
// actually changed. Rebuilding rows through innerHTML in here is exactly
// the mistake app_assets_test.go's TestGridUpdatePathNeverRebuildsRowsWithInnerHTML
// guards against, because it is invisible on review and would throw away
// the 4.8 ms figure this renderer exists for (docs/SPECS.md section 10.1).
function layout() {
  const sc = document.querySelector(".gridScroll");
  if (!sc) return;
  ensureSpacers();

  const total = data.length;
  const first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - OVERSCAN);
  const visible = Math.ceil(sc.clientHeight / ROW_H) + OVERSCAN * 2;
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
  $("dot").setAttribute("aria-label", live ? "connected" : "disconnected");
  if (st.sqltop) $("build").textContent = st.sqltop;
  $("instance").textContent = st.instance || "connecting...";
  $("version").textContent = st.version || "";
  $("message").textContent = st.message || "";
  $("rowCount").textContent = data.length + " requests";
  $("seq").textContent = "tick " + seq;
}

const es = new EventSource("/api/stream?t=" + encodeURIComponent(token));
es.addEventListener("snapshot", (e) => {
  const p = JSON.parse(e.data);
  if (p.refs) for (const [k, v] of Object.entries(p.refs)) refs.set(k, v);
  data = p.rows || [];
  layout();
  applyStatus(p.status || {}, p.seq);
});
es.addEventListener("error", () => {
  $("dot").classList.remove("live");
  $("dot").setAttribute("aria-label", "disconnected");
  $("message").textContent = "lost the connection to sqltop, retrying";
});

document.querySelector(".gridScroll").addEventListener("scroll", layout, { passive: true });
head();
