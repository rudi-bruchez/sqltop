// Drives the real page in a real browser over the Chrome DevTools Protocol
// and prints one JSON object of observations. internal/web/e2e_test.go does
// the asserting; this file only looks.
//
// Deno rather than a Go WebSocket library: the protocol needs a WebSocket,
// Go's standard library has none, and this project already depends on deno
// for the linter and the filter-logic test. A test dependency that is
// already there beats a new one in go.mod.
//
//   deno run --allow-net e2e-driver.js <pageURL> <cdpPort>
"use strict";

const pageURL = Deno.args[0];
const cdpPort = Deno.args[1];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const rpc = (ws) => {
  let id = 0;
  const pending = new Map();
  ws.addEventListener("message", (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
  });
  return (method, params) => new Promise((resolve) => {
    const n = ++id;
    pending.set(n, resolve);
    ws.send(JSON.stringify({ id: n, method, params: params || {} }));
  });
};

const target = await (await fetch(
  `http://127.0.0.1:${cdpPort}/json/new?${encodeURIComponent(pageURL)}`,
  { method: "PUT" },
)).json();

const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  ws.addEventListener("open", resolve);
  ws.addEventListener("error", reject);
});
const send = rpc(ws);

// Anything the browser complains about. A blocked subresource is a network
// failure rather than a console error, so both are watched: that is the
// class of bug that shipped once already, when the page could not load its
// own stylesheet because a relative URL does not inherit a query string.
const problems = [];
ws.addEventListener("message", (e) => {
  const m = JSON.parse(e.data);
  if (m.method === "Runtime.exceptionThrown") {
    problems.push("exception: " + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
  }
  if (m.method === "Log.entryAdded" && m.params.entry.level === "error") {
    problems.push("log: " + m.params.entry.text + " " + (m.params.entry.url || ""));
  }
  if (m.method === "Network.loadingFailed" && !m.params.canceled) {
    problems.push("request failed: " + m.params.errorText + " " + (m.params.type || ""));
  }
});

await send("Runtime.enable");
await send("Log.enable");
await send("Network.enable");
await send("Page.enable");
await send("Page.navigate", { url: pageURL });
await send("Emulation.setDeviceMetricsOverride", { width: 1600, height: 1000, deviceScaleFactor: 1, mobile: false });

const ev = async (expr) => {
  const r = await send("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.result.exceptionDetails) {
    throw new Error("evaluate failed: " + JSON.stringify(r.result.exceptionDetails));
  }
  return r.result.result.value;
};
const json = async (expr) => JSON.parse(await ev("JSON.stringify(" + expr + ")"));

// Wait for the stream rather than for a fixed delay: a fixed delay is a
// flaky test on a loaded machine.
let ticks = 0;
for (let i = 0; i < 120; i++) {
  await sleep(250);
  try {
    ticks = await ev("(typeof view === 'undefined') ? 0 : view.length");
  } catch { /* the page is not up yet */ }
  if (ticks > 0) break;
}

const out = { problems, rowsSeen: ticks };

out.identity = await json(`(() => {
  const shown = {};
  for (const row of document.querySelectorAll("#serverInfo .si")) {
    if (row.hidden) continue;
    shown[row.querySelector("dt").textContent.trim()] = row.querySelector("dd").textContent.trim();
  }
  return shown;
})()`);

out.page = await json(`({
  linkTags: document.querySelectorAll("link[rel=stylesheet]").length,
  scriptSrc: document.querySelectorAll("script[src]").length,
  styleTags: document.querySelectorAll("style").length,
  iconIsData: (document.querySelector("link[rel=icon]") || {}).href?.startsWith("data:") || false,
  headers: [...document.querySelectorAll("#gridHead th")].length,
  filterBoxes: [...document.querySelectorAll(".filterBox")].length,
  sortButtons: [...document.querySelectorAll(".sortBtn")].length,
  groups: [...document.querySelectorAll(".figGroup")].map((g) => g.id),
  tiles: document.querySelectorAll(".tile").length,
  hasPlanCache: !!document.getElementById("v-plan_cache_mb"),
  hasBufferPool: !!document.getElementById("v-buffer_pool_mb"),
  memoryFolded: !document.getElementById("g-memory").open,
  pageScrolls: document.documentElement.scrollHeight > innerHeight + 2,
  statusBarVisible: document.getElementById("statusBar").getBoundingClientRect().bottom <= innerHeight + 1
})`);

out.honesty = await json(`(() => {
  const t = (k) => {
    const el = document.getElementById("v-" + k);
    return el ? { text: el.textContent.trim(), na: el.classList.contains("na") } : null;
  };
  return { available: t("page_life_expectancy"), unavailable: t("other_cpu_percent"), absent: t("buffer_pool_mb") };
})()`);

// A figure that alternates between a reading and nothing, watched across
// ticks. A tile that quietly kept its last value would be invisible to any
// single observation, because it would still look like a plausible number.
{
  const seen = { withValue: 0, greyed: 0, staleWhileGreyed: [] };
  for (let i = 0; i < 40; i++) {
    const o = await json(`(() => {
      const el = document.getElementById("v-buffer_cache_hit_ratio");
      return { text: el.textContent.trim(), na: el.classList.contains("na") };
    })()`);
    if (o.na) {
      seen.greyed++;
      if (o.text !== "n/a") seen.staleWhileGreyed.push(o.text);
    } else if (/[0-9]/.test(o.text)) {
      seen.withValue++;
    }
    await sleep(120);
  }
  out.flipping = seen;
}

// Folding a group must hand its height back to the grid.
out.fold = await json(`(() => {
  const sc = document.querySelector(".gridScroll");
  const before = sc.clientHeight;
  // A group the configuration left open, so folding it actually changes
  // something: g-memory starts folded in this fixture and folding it again
  // would measure nothing.
  const g = document.getElementById("g-cpu");
  g.open = false;
  g.dispatchEvent(new Event("toggle"));
  const afterGroup = sc.clientHeight;
  const d = document.getElementById("dashboard");
  d.open = false;
  d.dispatchEvent(new Event("toggle"));
  const afterAll = sc.clientHeight;
  d.open = true; g.open = true;
  d.dispatchEvent(new Event("toggle"));
  return { before, afterGroup, afterAll };
})()`);

const cpuOf = `(r) => val(r, "cpu")`;
out.sort = await json(`(() => {
  const asc = (a) => a.every((v, i) => i === 0 || a[i - 1] <= v);
  const desc = (a) => a.every((v, i) => i === 0 || a[i - 1] >= v);
  const cpus = () => view.map(${cpuOf});
  const seen = [];
  setSort("cpu_ms"); seen.push({ dir: sortDir, ordered: asc(cpus()) });
  setSort("cpu_ms"); seen.push({ dir: sortDir, ordered: desc(cpus()) });
  setSort("cpu_ms"); seen.push({ dir: sortDir, field: sortField });
  return seen;
})()`);

const setFilter = (field, text) => ev(`(() => {
  const el = document.getElementById("f-${field}");
  el.value = ${JSON.stringify(text)};
  el.dispatchEvent(new Event("input", { bubbles: true }));
  return "ok";
})()`);

// The clear button appears only when there is something to clear, and
// clearing through it must leave the same state as clearing by hand.
out.clearButton = await json(`(() => {
  const x = document.getElementById("x-database");
  return { hiddenWhenEmpty: x.hidden };
})()`);

await setFilter("database", "alpha");
out.clearButton.shownWhenFiltered = await ev(`!document.getElementById("x-database").hidden`);

out.filterOne = await json(`({ rows: view.length, total: data.length,
  allMatch: view.every((r) => val(r, "db").toLowerCase().includes("alpha")),
  boxMarked: document.getElementById("f-database").classList.contains("on"),
  rowCount: document.getElementById("rowCount").textContent })`);

await setFilter("cpu_ms", ">5000");
out.filterAnd = await json(`({ rows: view.length,
  allMatch: view.every((r) => val(r, "db").toLowerCase().includes("alpha") && val(r, "cpu") > 5000) })`);

await setFilter("cpu_ms", "");
// The db filter goes through the cross rather than through the keyboard,
// so the button is exercised as a user would use it.
out.clearedByButton = await json(`(() => {
  document.getElementById("x-database").dispatchEvent(new MouseEvent("click", { bubbles: true }));
  const el = document.getElementById("f-database");
  return { boxValue: el.value, boxMarked: el.classList.contains("on"),
           buttonHidden: document.getElementById("x-database").hidden,
           rows: view.length, total: data.length };
})()`);
out.filterCleared = await json(`({ rows: view.length, total: data.length })`);

// The anchoring rule of section 8.1, both branches, in one evaluation so
// session churn cannot take the selected row away between the steps.
// The row is chosen at the top of the list and the view is then scrolled to
// the far end before the filter is applied, so that leaving the scroll
// where it was would put the row off screen. Selecting a row near where the
// scroll already is would pass whether or not anything re-anchored.
out.anchorKeeps = await json(`(() => {
  const sc = document.querySelector(".gridScroll");
  sc.scrollTop = 0;
  layout();
  const rows = [...document.querySelectorAll("#gridBody tr")].filter((r) => r.children.length > 1 && !r.hidden);
  rows[1].dispatchEvent(new MouseEvent("click", { bubbles: true }));
  const db = val(view.find((r) => rowKey(r) === selectedKey), "db");
  sc.scrollTop = sc.scrollHeight;
  layout();
  const scrollBefore = Math.round(sc.scrollTop);
  const el = document.getElementById("f-database");
  el.value = db;
  el.dispatchEvent(new Event("input", { bubbles: true }));
  const sel = document.querySelector("#gridBody tr.sel");
  const visible = sel ? (() => {
    const a = sel.getBoundingClientRect(), b = sc.getBoundingClientRect();
    return a.top >= b.top - 1 && a.bottom <= b.bottom + 1;
  })() : false;
  return { filteredOn: db, rows: view.length, scrollBefore,
           scrollAfter: Math.round(sc.scrollTop), stillPresent: view.some((r) => rowKey(r) === selectedKey),
           marked: !!sel, visible };
})()`);

out.anchorDrops = await json(`(() => {
  const sc = document.querySelector(".gridScroll");
  sc.scrollTop = 400;
  const el = document.getElementById("f-database");
  el.value = "no-such-database-anywhere";
  el.dispatchEvent(new Event("input", { bubbles: true }));
  return { rows: view.length, scrollAfter: Math.round(sc.scrollTop) };
})()`);

// The column selection of spec section 8.2, from the file to the screen and
// back: what the configuration asked for, what the panel offers, what
// dragging a heading does, and what ticking a box does. Last, so none of it
// can disturb the assertions above.
out.columns = await json(`(() => {
  const order = () => [...document.querySelectorAll("#gridHead th")].map((th) => th.dataset.f);
  const boxes = () => [...document.querySelectorAll("#colList input")].map((i) => ({ f: i.dataset.f, on: i.checked }));
  const configured = order();
  const panel = boxes();
  // anchorDrops left a filter that matches nothing, and an empty grid has
  // an empty row pool, which would make the cell count below vacuous.
  setFilter(document.getElementById("f-database"), "");

  moveColumn("spid", "database");
  const afterDrag = order();

  // Switching a column back on changes the column count, which is the one
  // change the per-cell update path cannot absorb: measured here rather
  // than after the drag, where the count does not move and the reading
  // would be true whatever applyColumns did with the pool.
  const host = [...document.querySelectorAll("#colList input")].find((i) => i.dataset.f === "host");
  host.checked = true;
  host.dispatchEvent(new Event("change"));

  return { configured, panel, afterDrag, poolCells: pool.length ? pool[0].tds.length : -1,
           rows: view.length, afterShow: order() };
})()`);

// The tabs of spec section 7. Each list view is filled by its own request,
// made only while its tab is open, so this is also the check that the tab
// actually asks for anything.
const key = (k) => `globalThis.dispatchEvent(new KeyboardEvent("keydown", { key: ${JSON.stringify(k)}, bubbles: true }))`;
out.views = { tabs: await json(`[...document.querySelectorAll("#tabs button")].map((b) => b.dataset.v)`) };

await ev(key("b"));
out.views.blocking = await json(`(() => ({
  rows: view.length,
  total: data.length,
  gridVisible: !document.querySelector(".gridScroll").hidden,
  allInAChain: view.every((r) => val(r, "by") || view.some((o) => val(o, "by") === val(r, "spid"))),
  depthShown: [...document.querySelectorAll("#gridHead th")].some((th) => th.dataset.f === "blocking_depth"),
}))()`);

await ev(key("u"));
await sleep(700);
out.views.sessions = await json(`(() => {
  const p = document.getElementById("panel-sessions");
  return {
    visible: !p.hidden,
    gridHidden: document.querySelector(".gridScroll").hidden,
    headings: [...p.querySelectorAll("th")].map((th) => th.textContent),
    rows: p.querySelectorAll("tbody tr").length,
    firstRow: [...(p.querySelectorAll("tbody tr")[0] || { cells: [] }).cells].map((td) => td.textContent),
  };
})()`);

await ev(key("x"));
await sleep(700);
out.views.transactions = await json(`(() => {
  const p = document.getElementById("panel-transactions");
  const tables = [...p.querySelectorAll("table")];
  return {
    visible: !p.hidden,
    tables: tables.length,
    tranRows: tables[0] ? tables[0].querySelectorAll("tbody tr").length : 0,
    lockRows: tables[1] ? tables[1].querySelectorAll("tbody tr").length : 0,
    lockText: tables[1] ? tables[1].textContent : "",
  };
})()`);

await ev(key("l"));
await sleep(700);
out.views.logs = await json(`(() => {
  const p = document.getElementById("panel-logs");
  return { visible: !p.hidden, rows: p.querySelectorAll("tbody tr").length, text: p.textContent };
})()`);

// The column panel follows the tab: it is per view, and offering the grid's
// columns while a log list is on screen would be worse than useless.
out.views.panelFollows = await json(`(() => {
  const before = document.getElementById("colWhich").textContent;
  return { which: before, fields: [...document.querySelectorAll("#colList input")].map((i) => i.dataset.f) };
})()`);

await ev(key("r"));
await sleep(400);
out.views.backToGrid = await ev(`!document.querySelector(".gridScroll").hidden && view.length === data.length`);

// The single-keypress commands of spec section 7, pressed the way a person
// presses them: a keydown on the window, not a call to the function behind
// it, so the dispatch and the guard against typing in a filter box are both
// under test.
out.commands = {};

await ev(key("h"));
out.commands.help = await json(`({ open: document.getElementById("helpDialog").open, entries: document.querySelectorAll("#helpList dt").length })`);
await ev(key("h"));
out.commands.helpClosed = await ev(`!document.getElementById("helpDialog").open`);

// A letter typed into a filter box is a letter, not a command.
await ev(`document.getElementById("f-database").focus()`);
await ev(`document.getElementById("f-database").dispatchEvent(new KeyboardEvent("keydown", { key: "p", bubbles: true }))`);
out.commands.pausedByTyping = await ev(`paused`);
await ev(`document.getElementById("f-database").blur()`);

await ev(key("p"));
const frozenAt = await ev(`document.getElementById("seq").textContent`);
await sleep(900);
out.commands.pause = {
  on: await ev(`paused`),
  marked: await ev(`!document.getElementById("pauseMark").hidden`),
  before: frozenAt,
  after: await ev(`document.getElementById("seq").textContent`),
};
await ev(key("p"));
await sleep(900);
out.commands.resumed = { on: await ev(`paused`), seq: await ev(`document.getElementById("seq").textContent`) };

await ev(key("s"));
await sleep(900);
out.commands.snapshotMessage = await ev(`document.getElementById("notice").textContent`);
out.commands.rowsWhenSaved = await ev(`view.length`);

// Last, because it slows the stream down for everything after it.
await ev(key("f"));
await sleep(2500);
out.commands.rate = await ev(`document.getElementById("rate").textContent`);

console.log(JSON.stringify(out));
ws.close();
Deno.exit(0);
