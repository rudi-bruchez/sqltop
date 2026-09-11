// The connect page: builds the form from what the server says this binary
// can do, posts it, and hands over to the monitor on the same address.
"use strict";

const $ = (id) => document.getElementById(id);
const q = "?t=" + encodeURIComponent(new URLSearchParams(location.search).get("t") || "");
let methods = [];

function method() {
  const r = document.querySelector('input[name="auth"]:checked');
  return methods.find((m) => m.id === (r && r.value)) || methods[0] || { id: "", fields: [] };
}

function say(id, text) {
  $(id).textContent = text;
  $(id).hidden = !text;
}

// Only the chosen method's fields are on screen, and only they are sent.
function showFields() {
  const m = method();
  const f = new Set(m.fields || []);
  $("loginRow").hidden = !f.has("login");
  $("passwordRow").hidden = !f.has("password");
  $("login").placeholder = m.id === "domain" ? "DOMAIN\\user" : "";
  say("methodNote", m.note || "");
  $("saveNote").hidden = !f.has("password") || $("saveRow").hidden;
  $("trustRow").hidden = $("encrypt").value !== "true";
}

function setup(opts) {
  methods = opts.methods || [];
  for (const [i, m] of methods.entries()) {
    const input = document.createElement("input");
    input.type = "radio";
    input.name = "auth";
    input.value = m.id;
    input.checked = i === 0;
    input.addEventListener("change", showFields);
    const label = document.createElement("label");
    label.append(input, " " + m.label);
    $("methods").append(label);
  }
  $("envPath").textContent = opts.env_path || ".env";
  // A saved string nobody reads would be reported as saved and change nothing.
  $("saveRow").hidden = opts.save === false;
  $("saveOff").hidden = opts.save !== false;
  showFields();
}

// Only the monitor answers /api/status, so its first 200 means the handover
// is done; a network error is the handover still in progress.
function waitForMonitor() {
  fetch("/api/status" + q)
    .then((r) => (r.ok ? location.assign("/" + q) : setTimeout(waitForMonitor, 250)))
    .catch(() => setTimeout(waitForMonitor, 250));
}

function submit(e) {
  e.preventDefault();
  const m = method();
  const f = new Set(m.fields || []);
  const server = $("server").value;
  const body = {
    server: server,
    auth: m.id,
    login: f.has("login") ? $("login").value : "",
    password: f.has("password") ? $("password").value : "",
    database: $("database").value,
    encrypt: $("encrypt").value,
    trust: $("encrypt").value === "true" && $("trust").checked,
    save_env: !$("saveRow").hidden && $("saveEnv").checked,
  };
  say("error", "");
  say("hint", "");
  // Without a port, a named instance goes through SQL Server Browser, which
  // takes the attempt's whole deadline to fail when a firewall drops it.
  say("status", server.includes("\\") && !server.includes(",")
    ? "resolving the instance through SQL Server Browser; if it does not answer, this fails after 30 seconds"
    : "connecting");
  $("go").disabled = true;
  fetch("/api/connect" + q, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
    .then((r) => r.json().then((j) => (r.ok ? j : Promise.reject(j))))
    .then((j) => {
      say("status", "connected to " + j.dsn + (j.env_written ? ", saved to " + j.env_written : ""));
      if (j.env_error) say("error", "could not write the .env file: " + j.env_error);
      waitForMonitor();
    })
    .catch((j) => {
      $("go").disabled = false;
      say("status", "");
      say("error", (j && (j.error || j.message)) || String(j));
      say("hint", (j && j.hint) || "");
    });
}

fetch("/api/connect" + q)
  .then((r) => r.json())
  .then(setup)
  .catch((e) => say("error", "could not load the form: " + e.message));
$("connectForm").addEventListener("submit", submit);
$("encrypt").addEventListener("change", showFields);
