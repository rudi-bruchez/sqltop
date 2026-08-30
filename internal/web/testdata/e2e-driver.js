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
  const g = document.getElementById("g-memory");
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
  setSort("cpu"); seen.push({ dir: sortDir, ordered: asc(cpus()) });
  setSort("cpu"); seen.push({ dir: sortDir, ordered: desc(cpus()) });
  setSort("cpu"); seen.push({ dir: sortDir, field: sortField });
  return seen;
})()`);

const setFilter = (field, text) => ev(`(() => {
  const el = document.getElementById("f-${field}");
  el.value = ${JSON.stringify(text)};
  el.dispatchEvent(new Event("input", { bubbles: true }));
  return "ok";
})()`);

await setFilter("db", "alpha");
out.filterOne = await json(`({ rows: view.length, total: data.length,
  allMatch: view.every((r) => val(r, "db").toLowerCase().includes("alpha")),
  boxMarked: document.getElementById("f-db").classList.contains("on"),
  rowCount: document.getElementById("rowCount").textContent })`);

await setFilter("cpu", ">5000");
out.filterAnd = await json(`({ rows: view.length,
  allMatch: view.every((r) => val(r, "db").toLowerCase().includes("alpha") && val(r, "cpu") > 5000) })`);

await setFilter("cpu", "");
await setFilter("db", "");
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
  const el = document.getElementById("f-db");
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
  const el = document.getElementById("f-db");
  el.value = "no-such-database-anywhere";
  el.dispatchEvent(new Event("input", { bubbles: true }));
  return { rows: view.length, scrollAfter: Math.round(sc.scrollTop) };
})()`);

console.log(JSON.stringify(out));
ws.close();
Deno.exit(0);
