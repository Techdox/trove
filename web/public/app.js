"use strict";

// Trove dashboard — polls the read-only APIs and renders. No dependencies.
const POLL_MS = 10000;

const $ = (id) => document.getElementById(id);

async function fetchJSON(url) {
  const res = await fetch(url, { headers: { Accept: "application/json" } });
  if (!res.ok) throw new Error(`${url} -> ${res.status}`);
  return res.json();
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

// Relative time from an RFC3339 string.
function relTime(iso) {
  if (!iso) return "never";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  let s = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m ago`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h ago`;
}

// ---- badge mapping -------------------------------------------------------

const HEALTH_CLASS = {
  healthy: "b-green",
  unhealthy: "b-red",
  stale: "b-yellow",
  unknown: "b-gray",
};

const AGENT_CLASS = { ok: "b-green", stale: "b-yellow", offline: "b-red", unknown: "b-gray" };

function stateClass(state) {
  switch (state) {
    case "running": return "b-green";
    case "exited":
    case "dead":
    case "removed": return "b-red";
    case "created":
    case "paused":
    case "restarting": return "b-yellow";
    default: return "b-gray";
  }
}

function badge(cls, label) {
  return `<span class="badge ${cls}">${esc(label)}</span>`;
}

function imageHTML(image) {
  if (!image) return '<span class="muted">—</span>';
  const i = image.lastIndexOf(":");
  // Treat as tag only if the colon is after the last "/" (not a registry port).
  const slash = image.lastIndexOf("/");
  if (i > slash && i !== -1) {
    return `${esc(image.slice(0, i))}<span class="tag">:${esc(image.slice(i + 1))}</span>`;
  }
  return esc(image);
}

function portsHTML(ports) {
  if (!Array.isArray(ports) || ports.length === 0) return '<span class="muted">—</span>';
  return ports
    .map((p) => (p.host ? `${p.host}→${p.container}` : `${p.container}`) + `/${esc(p.proto || "tcp")}`)
    .join(" ");
}

// ---- rendering -----------------------------------------------------------

function renderAgents(data) {
  const el = $("agents");
  const agents = data.agents || [];
  if (agents.length === 0) {
    el.innerHTML = '<div class="empty">No agents registered. Create one with <code>trove-server agent create &lt;name&gt;</code>.</div>';
    return;
  }
  el.innerHTML = agents.map((a) => {
    const st = a.status || "unknown";
    return `<div class="agent-card ${esc(st)}">
      <div class="row">
        <span class="name">${esc(a.name)}</span>
        ${badge(AGENT_CLASS[st] || "b-gray", st)}
      </div>
      <div class="meta">${esc(a.platform || "—")}${a.version ? " · v" + esc(a.version) : ""}</div>
      <div class="meta">last push: ${esc(relTime(a.last_seen_at))}</div>
    </div>`;
  }).join("");
}

const FRESHNESS = {
  current: '<span class="badge b-green">up to date</span>',
  outdated: '<span class="badge b-peach">update</span>',
};

function freshnessCell(s) {
  if (s.freshness === "outdated") {
    return `<span class="badge b-peach" title="latest: ${esc(s.latest_digest || "")}">update</span>`;
  }
  return FRESHNESS[s.freshness] || '<span class="muted">—</span>';
}

function serviceRow(s, isChild) {
  const removed = s.state === "removed";
  const cls = [removed ? "removed" : "", isChild ? "child" : ""].filter(Boolean).join(" ");
  const name = (isChild ? '<span class="tree">└─</span> ' : "") + esc(s.name || s.external_id);
  return `<tr class="${cls}">
    <td><span class="svc-name">${name}</span> <span class="kind">${esc(s.kind || "")}</span></td>
    <td class="image">${imageHTML(s.image)}</td>
    <td>${badge(stateClass(s.state), s.state || "?")}</td>
    <td>${badge(HEALTH_CLASS[s.health] || "b-gray", s.health || "unknown")}</td>
    <td>${freshnessCell(s)}</td>
    <td class="ports">${portsHTML(s.ports)}</td>
    <td class="muted nowrap">${esc(relTime(s.last_seen_at))}</td>
  </tr>`;
}

function renderHosts(data) {
  const el = $("hosts");
  const hosts = data.hosts || [];
  if (hosts.length === 0) {
    el.innerHTML = '<div class="host"><div class="empty">No services reported yet.</div></div>';
    return;
  }
  el.innerHTML = hosts.map((h) => {
    const svcs = h.services || [];
    const ids = new Set(svcs.map((s) => s.external_id));
    const childrenByParent = {};
    for (const s of svcs) {
      if (s.parent_external_id && ids.has(s.parent_external_id)) {
        (childrenByParent[s.parent_external_id] ||= []).push(s);
      }
    }
    // Top level = services with no (resolvable) parent. Orphans whose parent
    // isn't present still surface here rather than vanishing.
    const topLevel = svcs.filter((s) => !s.parent_external_id || !ids.has(s.parent_external_id));
    const rows = topLevel.map((s) => {
      let out = serviceRow(s, false);
      const kids = childrenByParent[s.external_id];
      if (kids) out += kids.map((k) => serviceRow(k, true)).join("");
      return out;
    }).join("");

    const st = h.agent_status || "unknown";
    return `<div class="host">
      <div class="host-head">
        <span class="hostname">${esc(h.hostname)}</span>
        <span class="sub">${esc(h.agent)} · ${esc(h.platform || "—")}</span>
        ${badge(AGENT_CLASS[st] || "b-gray", st)}
        <span class="count">${svcs.length} service(s)</span>
      </div>
      <table>
        <thead><tr>
          <th>Service</th><th>Image</th><th>State</th><th>Health</th><th>Freshness</th><th>Ports</th><th>Last seen</th>
        </tr></thead>
        <tbody>${rows || ""}</tbody>
      </table>
    </div>`;
  }).join("");
}

function renderEvents(data) {
  const el = $("events");
  const events = data.events || [];
  if (events.length === 0) {
    el.innerHTML = '<div class="empty">No recent state changes.</div>';
    return;
  }
  el.innerHTML = events.slice(0, 40).map((e) => {
    const from = e.from_state || "∅";
    return `<div class="event-row">
      <span class="when nowrap">${esc(relTime(e.at))}</span>
      <span class="what"><strong>${esc(e.service)}</strong> <span class="muted">@ ${esc(e.hostname)}</span>
        &nbsp;${esc(from)} <span class="arrow">→</span> ${esc(e.to_state)}</span>
    </div>`;
  }).join("");
}

// ---- poll loop -----------------------------------------------------------

function setStatus(ok, msg) {
  const pulse = $("pulse");
  const err = $("error");
  if (ok) {
    pulse.className = "pulse";
    $("updated").textContent = "updated " + new Date().toLocaleTimeString();
    err.classList.remove("show");
  } else {
    pulse.className = "pulse error";
    err.textContent = "⚠ " + msg + " — retrying…";
    err.classList.add("show");
  }
}

async function refresh() {
  try {
    const [services, agents, events] = await Promise.all([
      fetchJSON("api/v1/services"),
      fetchJSON("api/v1/agents"),
      fetchJSON("api/v1/events?limit=40"),
    ]);
    renderAgents(agents);
    renderHosts(services);
    renderEvents(events);
    setStatus(true);
  } catch (e) {
    setStatus(false, e.message || "connection lost");
  }
}

refresh();
setInterval(refresh, POLL_MS);
