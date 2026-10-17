// Effort-router page. Static: data comes only from the authenticated Management API,
// and every value is rendered as text.
"use strict";
const EFFORTS = ["low", "medium", "high", "xhigh"];
const API = "/v0/management/effort-router/decisions";
const $ = (id) => document.getElementById(id);

function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = String(text);
  if (cls) e.className = cls;
  return e;
}

function row(cells, header) {
  const tr = el("tr");
  for (const [text, cls] of cells) tr.append(el(header ? "th" : "td", text, cls));
  return tr;
}

function bar(counts) {
  const total = EFFORTS.reduce((n, e) => n + (counts[e] || 0), 0) || 1;
  const b = el("div", undefined, "bar");
  b.style.width = "100%";
  for (const e of EFFORTS) {
    if (!counts[e]) continue;
    const s = el("span", undefined, e);
    s.style.width = (100 * counts[e] / total) + "%";
    s.title = e + ": " + counts[e];
    b.append(s);
  }
  return b;
}

function render(d) {
  const status = $("status");
  status.replaceChildren(
    el("span", "Mode: " + d.mode, "badge"),
    el("span", "Classifier: " + (d.provider || "custom"), "badge"),
    el("span", "Claude baseline: " + d.baseline_effort, "badge"),
    el("span", "Codex baseline: " + d.codex_baseline_effort, "badge"),
    el("span", d.decisions + " turns since " + (d.truncated && d.coverage_from ? d.coverage_from : d.since), "muted"));
  if (d.truncated) {
    status.append(el("span", "Only the newest 4 MiB of the state file was read; it starts after the selected window" +
      (d.coverage_from ? ", at " + d.coverage_from : "") + ". Counts cover that shorter period.", "badge"));
  }

  const models = $("models");
  models.replaceChildren(row([["Model"], ...EFFORTS.map((e) => [e, "num"]), ["Changed", "num"], ["Mix"]], true));
  // The server reports "changed" only when both baselines agree; otherwise it is unknown here.
  for (const [m, counts] of Object.entries(d.by_model)) {
    const total = EFFORTS.reduce((n, e) => n + (counts[e] || 0), 0);
    const changed = d.changed ? d.changed[m] || 0 : null;
    const tr = row([[m], ...EFFORTS.map((e) => [counts[e] || 0, "num"]), [total && changed !== null ? Math.round(100 * changed / total) + "%" : "-", "num"]]);
    const td = el("td"); td.append(bar(counts)); tr.append(td);
    models.append(tr);
  }
  if (Object.keys(d.shadow_by_model).length) {
    for (const [m, counts] of Object.entries(d.shadow_by_model)) {
      const tr = row([[m + " (shadow)"], ...EFFORTS.map((e) => [counts[e] || 0, "num"]), ["-", "num"]]);
      const td = el("td"); td.append(bar(counts)); tr.append(td);
      models.append(tr);
    }
  }

  const hourly = $("hourly");
  hourly.replaceChildren(row([["Hour (UTC)"], ...EFFORTS.map((e) => [e, "num"]), ["Errors", "num"], ["Mix"]], true));
  for (const h of d.hourly.slice().reverse()) {
    const tr = row([[h.hour.replace("T", " ").replace(":00:00Z", ":00")], ...EFFORTS.map((e) => [h.efforts[e] || 0, "num"]), [h.errors, "num"]]);
    const td = el("td"); td.append(bar(h.efforts)); tr.append(td);
    hourly.append(tr);
  }

  const c = $("classifier");
  const lat = d.latency_ms;
  c.replaceChildren(el("span", lat.p50 === undefined ? "No classified turns" : "Latency p50 " + lat.p50 + " ms, p95 " + lat.p95 + " ms, max " + lat.max + " ms", "badge"));
  const errs = Object.entries(d.errors);
  c.append(el("span", errs.length ? "Errors: " + errs.map(([k, v]) => k + " " + v).join(", ") : "No classifier errors", "badge"));

  const recent = $("recent");
  recent.replaceChildren(row([["Time (UTC)"], ["Model"], ["Effort"], ["ms", "num"], ["Note"]], true));
  for (const r of d.recent) {
    recent.append(row([[r.at.replace("T", " ").replace("Z", "")], [r.model], [r.effort, r.effort], [r.ms, "num"], [r.err || (r.shadow ? "shadow" : "")]]));
  }
  $("out").hidden = false;
}

async function load() {
  const key = sessionStorage.getItem("effort-router-key") || "";
  if (!key) return;
  $("error").textContent = "";
  try {
    const res = await fetch(API + "?hours=" + encodeURIComponent($("hours").value), {
      headers: { "Authorization": "Bearer " + key, "X-Management-Key": key }, cache: "no-store", credentials: "omit",
    });
    if (res.status === 401 || res.status === 403) throw new Error("The management key was not accepted.");
    if (!res.ok) throw new Error("Request failed: HTTP " + res.status);
    render(await res.json());
  } catch (e) {
    $("error").textContent = e.message;
  }
}

$("auth").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const key = $("key").value.trim();
  if (key) sessionStorage.setItem("effort-router-key", key);
  $("key").value = "";
  load();
});
$("hours").addEventListener("change", load);
load();
setInterval(load, 60000);
