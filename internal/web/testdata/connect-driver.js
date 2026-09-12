// Drives the connect page over the Chrome DevTools Protocol and prints one
// JSON object of observations. connect_e2e_test.go does the asserting.
//
//   deno run --allow-net connect-driver.js <pageURL> <cdpPort>
"use strict";

const pageURL = Deno.args[0];
const cdpPort = Deno.args[1];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const target = await (await fetch(
  `http://127.0.0.1:${cdpPort}/json/new?${encodeURIComponent(pageURL)}`,
  { method: "PUT" },
)).json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  ws.addEventListener("open", resolve);
  ws.addEventListener("error", reject);
});

let id = 0;
const pending = new Map();
const problems = [];
ws.addEventListener("message", (e) => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) {
    pending.get(m.id)(m);
    pending.delete(m.id);
  }
  if (m.method === "Runtime.exceptionThrown") {
    problems.push("exception: " + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
  }
  // The 502 of the refused attempt and the 404s of the handover poll are
  // expected, and the browser logs both as errors.
  if (m.method === "Log.entryAdded" && m.params.entry.level === "error" && !(m.params.entry.url || "").includes("/api/")) {
    problems.push("log: " + m.params.entry.text + " " + (m.params.entry.url || ""));
  }
});
const send = (method, params) => new Promise((resolve) => {
  const n = ++id;
  pending.set(n, resolve);
  ws.send(JSON.stringify({ id: n, method, params: params || {} }));
});

await send("Runtime.enable");
await send("Log.enable");
await send("Page.enable");
await send("Page.navigate", { url: pageURL });

const ev = async (expr) => {
  const r = await send("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.result.exceptionDetails) throw new Error("evaluate failed: " + JSON.stringify(r.result.exceptionDetails));
  return r.result.result.value;
};
const json = async (expr) => JSON.parse(await ev("JSON.stringify(" + expr + ")"));
const waitFor = async (expr, ms) => {
  for (let i = 0; i < ms / 100; i++) {
    try {
      if (await ev(expr)) return true;
    } catch { /* between two documents */ }
    await sleep(100);
  }
  return false;
};

const out = { problems };
await waitFor(`document.querySelectorAll('input[name="auth"]').length > 0`, 10000);
out.methods = await json(`[...document.querySelectorAll('input[name="auth"]')].map((r) => r.value)`);

out.fields = {};
for (const m of out.methods) {
  await ev(`(() => { const r = document.querySelector('input[name="auth"][value="${m}"]'); r.checked = true; r.dispatchEvent(new Event("change")); })()`);
  out.fields[m] = await json(`({
    login: !document.getElementById("loginRow").hidden,
    password: !document.getElementById("passwordRow").hidden,
    note: document.getElementById("methodNote").hidden ? "" : document.getElementById("methodNote").textContent,
  })`);
}

out.trust = {};
for (const v of ["", "true", "strict"]) {
  await ev(`(() => { const s = document.getElementById("encrypt"); s.value = ${JSON.stringify(v)}; s.dispatchEvent(new Event("change")); })()`);
  out.trust[v || "default"] = await ev(`!document.getElementById("trustRow").hidden`);
}
await ev(`(() => { const s = document.getElementById("encrypt"); s.value = ""; s.dispatchEvent(new Event("change")); })()`);
await ev(`(() => { const r = document.querySelector('input[name="auth"][value="sql"]'); r.checked = true; r.dispatchEvent(new Event("change")); })()`);

const submit = (server) => ev(`(() => {
  document.getElementById("server").value = ${JSON.stringify(server)};
  document.getElementById("login").value = "dba";
  document.getElementById("password").value = "p@ss";
  document.getElementById("connectForm").requestSubmit();
})()`);

await submit("nope");
await waitFor(`!document.getElementById("error").hidden`, 5000);
out.failure = await json(`({
  error: document.getElementById("error").textContent,
  hint: document.getElementById("hint").textContent,
  enabled: !document.getElementById("go").disabled,
})`);
out.env = await json(`({
  path: document.getElementById("envPath").textContent,
  note: !document.getElementById("saveNote").hidden,
})`);

// The successful attempt also carries the two boxes a DBA ticks, so the
// test sees them reach the server rather than only appear on screen.
await ev(`(() => {
  const s = document.getElementById("encrypt");
  s.value = "true";
  s.dispatchEvent(new Event("change"));
  document.getElementById("trust").checked = true;
  document.getElementById("saveEnv").checked = true;
})()`);
await submit("db01");
out.landed = await waitFor(
  `document.getElementById("gridBody") !== null && [...document.querySelectorAll("#gridBody tr")].some((r) => r.children.length > 1 && !r.hidden)`,
  15000,
);

console.log(JSON.stringify(out));
ws.close();
Deno.exit(0);
