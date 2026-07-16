"use strict";

// Trove dashboard — polls the read-only APIs and renders. No dependencies.
//
// Data flows one way: refresh() fetches into state.data, render() projects
// state (filters, collapse, drawer, cursor) onto the DOM. UI interactions
// mutate state and call render(); the 10s poll only replaces state.data, so
// filter/drawer/collapse state survives refreshes.

const POLL_MS = 10000;

const $ = (id) => document.getElementById(id);

// ---------------------------------------------------------------- state ----

const state = {
  q: "",                 // filter text
  chips: new Set(),      // active quick filters (keys of CHIP_DEFS)
  showRemoved: false,    // include soft-removed services
  removedOnly: false,    // limit the catalogue to soft-removed services
  collapsed: new Set(),  // collapsed host keys
  drawerKey: null,       // key of the service open in the drawer
  hostDrawerKey: null,   // key of the host open in the drawer
  cursorKey: null,       // key of the keyboard-cursor row
  data: { services: null, agents: null, events: null },
};

const CHIP_DEFS = [
  { key: "running",   cls: "c-green",  test: (s) => s.state === "running" },
  { key: "unhealthy", cls: "c-red",    test: (s) => s.health === "unhealthy" },
  { key: "stopped",   cls: "c-yellow", test: (s) => STOPPED_STATES.has(s.state) },
  { key: "outdated",  cls: "c-peach",  test: (s) => s.freshness === "outdated" },
  { key: "stale",     cls: "c-gray",   test: (s) => s.health === "stale" },
];

// Platforms use different neutral stopped states. Proxmox reports `stopped`,
// while Docker/systemd report `exited`, `dead`, or `failed`. Trove surfaces all
// of them for investigation without assuming a powered-off guest is unhealthy.
const STOPPED_STATES = new Set(["exited", "dead", "failed", "stopped"]);

// A service is identified by agent + hostname + external_id (hostnames are
// only unique per agent).
const keyOf = (h, s) => `${h.agent}\u001f${h.hostname}\u001f${s.external_id}`;

// -------------------------------------------------------------- helpers ----

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

function absTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function agentVersionLabel(version) {
  if (!version) return "";
  return /^\d/.test(version) ? `v${version}` : version;
}

// ---------------------------------------------------------- badge maps ----

const HEALTH_CLASS = {
  healthy: "b-green",
  unhealthy: "b-red",
  stale: "b-gray",
  unknown: "b-gray",
};

const AGENT_CLASS = { ok: "b-green", stale: "b-yellow", offline: "b-red", unknown: "b-gray" };
const HOST_CONDITION_CLASS = { normal: "b-green", warning: "b-yellow", critical: "b-red", unknown: "b-gray" };

function stateClass(state) {
  // K8s parents report "ready/desired" (e.g. "2/2").
  const m = /^(\d+)\/(\d+)$/.exec(state || "");
  if (m) return Number(m[1]) >= Number(m[2]) && Number(m[2]) > 0 ? "b-green" : "b-yellow";
  switch (state) {
    case "running": return "b-green";
    case "exited":
    case "dead":
    case "failed":
    case "removed": return "b-red";
    case "created":
    case "paused":
    case "restarting": return "b-yellow";
    default: return "b-gray";
  }
}

function stateTextClass(state) {
  const b = stateClass(state);
  return { "b-green": "st-green", "b-red": "st-red", "b-yellow": "st-yellow" }[b] || "st-gray";
}

function badge(cls, label, extra) {
  return `<span class="badge ${cls}${extra ? " " + extra : ""}">${esc(label)}</span>`;
}

const PROXMOX_METRIC_KEYS = new Set([
  "node",
  "vmid",
  "ostype",
  "proxmox.cpu_pct",
  "proxmox.maxcpu",
  "proxmox.mem_used",
  "proxmox.mem_total",
  "proxmox.mem_pct",
  "proxmox.disk_used",
  "proxmox.disk_total",
  "proxmox.disk_pct",
  "proxmox.uptime",
]);

function proxmoxMetricRows(labels) {
  if (!labels || typeof labels !== "object") return [];
  const rows = [];
  if (labels.node) rows.push(["Node", labels.node]);
  if (labels.vmid) rows.push(["VMID", labels.vmid]);
  if (labels.ostype) rows.push(["OS type", labels.ostype]);
  if (labels["proxmox.cpu_pct"]) {
    const cores = labels["proxmox.maxcpu"] ? ` of ${labels["proxmox.maxcpu"]} cores` : "";
    rows.push(["CPU", `${labels["proxmox.cpu_pct"]}${cores}`]);
  } else if (labels["proxmox.maxcpu"]) {
    rows.push(["CPU", `${labels["proxmox.maxcpu"]} cores`]);
  }
  const mem = metricUsage(labels["proxmox.mem_used"], labels["proxmox.mem_total"], labels["proxmox.mem_pct"]);
  if (mem) rows.push(["Memory", mem]);
  const disk = metricUsage(labels["proxmox.disk_used"], labels["proxmox.disk_total"], labels["proxmox.disk_pct"]);
  if (disk) rows.push(["Disk", disk]);
  if (labels["proxmox.uptime"]) rows.push(["Uptime", labels["proxmox.uptime"]]);
  return rows;
}

function metricUsage(used, total, pct) {
  if (used && total && pct) return `${used} / ${total} · ${pct}`;
  if (used && total) return `${used} / ${total}`;
  if (total && pct) return `${pct} of ${total}`;
  return used || total || pct || "";
}

function isProxmoxService(host, svc) {
  return host.platform === "proxmox" || host.platform === "pve"
    || svc.labels?.["proxmox.cpu_pct"] !== undefined
    || svc.labels?.["proxmox.mem_pct"] !== undefined
    || svc.labels?.["proxmox.disk_pct"] !== undefined;
}

function hostPlatformLine(host) {
  const parts = [host.agent, host.platform || "—"];
  const meta = host.meta && typeof host.meta === "object" ? host.meta : {};
  if (meta["proxmox.version"]) {
    const release = meta["proxmox.release"] ? `-${meta["proxmox.release"]}` : "";
    parts.push(`Proxmox ${meta["proxmox.version"]}${release}`);
  }
  if (meta["docker.version"]) {
    parts.push(`Docker ${meta["docker.version"]}`);
  }
  if (meta["kubernetes.version"]) {
    parts.push(`Kubernetes ${meta["kubernetes.version"]}`);
  }
  return parts.filter(Boolean).join(" · ");
}

function finiteNumber(value) {
  if (value === null || value === undefined || value === "") return null;
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

function formatBytes(bytes) {
  const n = finiteNumber(bytes);
  if (n === null || n < 0) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  const precision = value >= 10 || unit === 0 ? 0 : 1;
  return `${value.toFixed(precision)} ${units[unit]}`;
}

function formatUptime(seconds) {
  const total = finiteNumber(seconds);
  if (total === null || total < 0) return "—";
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
}

function resourceMetric(label, usage) {
  if (!usage || typeof usage !== "object") return null;
  const used = finiteNumber(usage.used_bytes);
  const total = finiteNumber(usage.total_bytes);
  if (used === null || total === null || total <= 0 || used < 0 || used > total) return null;
  const percent = Math.round((used / total) * 100);
  return {
    label,
    value: `${percent}%`,
    title: `${label}: ${formatBytes(used)} / ${formatBytes(total)}`,
  };
}

function hostMetricItems(metrics) {
  if (!metrics || typeof metrics !== "object") return [];
  const items = [];
  const cpu = finiteNumber(metrics.cpu_usage_ratio);
  if (cpu !== null && cpu >= 0 && cpu <= 1) {
    const cores = finiteNumber(metrics.cpu_logical_count);
    items.push({
      label: "CPU",
      value: `${Math.round(cpu * 100)}%`,
      title: `CPU: ${Math.round(cpu * 100)}%${cores && cores > 0 ? ` · ${cores} logical CPUs` : ""}`,
    });
  }
  const memory = resourceMetric("RAM", metrics.memory);
  if (memory) items.push(memory);
  const rootDisk = resourceMetric("Root", metrics.root_disk);
  if (rootDisk) items.push(rootDisk);
  const load1 = finiteNumber(metrics.load_1);
  const load5 = finiteNumber(metrics.load_5);
  const load15 = finiteNumber(metrics.load_15);
  if ([load1, load5, load15].some((n) => n !== null && n >= 0)) {
    const values = [load1, load5, load15].map((n) => n === null || n < 0 ? "—" : n.toFixed(2));
    items.push({ label: "Load", value: values[0], title: `Load average: ${values.join(" · ")} (1m · 5m · 15m)` });
  }
  const uptime = finiteNumber(metrics.uptime_seconds);
  if (uptime !== null && uptime >= 0) {
    items.push({ label: "Up", value: formatUptime(uptime), title: `Uptime: ${formatUptime(uptime)}` });
  }
  return items;
}

function hostMetricsHTML(metrics) {
  const items = hostMetricItems(metrics);
  if (items.length === 0) return "";
  return `<span class="host-metrics" aria-label="Host resource metrics">${items.map((item) =>
    `<span class="host-metric" title="${esc(item.title)}"><span class="metric-label">${esc(item.label)}</span> ${esc(item.value)}</span>`
  ).join("")}</span>`;
}

function resourceMetricDetail(usage) {
  if (!usage || typeof usage !== "object") return "";
  const used = finiteNumber(usage.used_bytes);
  const total = finiteNumber(usage.total_bytes);
  if (used === null || total === null || total <= 0 || used < 0 || used > total) return "";
  return `${formatBytes(used)} / ${formatBytes(total)} · ${Math.round((used / total) * 100)}%`;
}

function hostMetricRows(metrics) {
  if (!metrics || typeof metrics !== "object") return [];
  const rows = [];
  const cpu = finiteNumber(metrics.cpu_usage_ratio);
  const cores = finiteNumber(metrics.cpu_logical_count);
  if (cpu !== null && cpu >= 0 && cpu <= 1) {
    rows.push(["CPU usage", `${Math.round(cpu * 100)}%${cores && cores > 0 ? ` · ${cores} logical CPUs` : ""}`]);
  } else if (cores && cores > 0) {
    rows.push(["Logical CPUs", String(cores)]);
  }
  const load = [metrics.load_1, metrics.load_5, metrics.load_15].map(finiteNumber);
  if (load.some((n) => n !== null && n >= 0)) {
    rows.push(["Load average", `${load.map((n) => n === null || n < 0 ? "—" : n.toFixed(2)).join(" · ")} (1m · 5m · 15m)`]);
  }
  const memory = resourceMetricDetail(metrics.memory);
  if (memory) rows.push(["Memory", memory]);
  const rootDisk = resourceMetricDetail(metrics.root_disk);
  if (rootDisk) rows.push(["Root disk", rootDisk]);
  const uptime = finiteNumber(metrics.uptime_seconds);
  if (uptime !== null && uptime >= 0) rows.push(["Uptime", formatUptime(uptime)]);
  return rows;
}

// ------------------------------------------------------- cell rendering ----

// splitImage separates "registry/namespace/name:tag" so the prefix can
// ellipsize while name:tag stays pinned and readable.
function splitImage(ref) {
  const slash = ref.lastIndexOf("/");
  const prefix = slash >= 0 ? ref.slice(0, slash + 1) : "";
  let rest = slash >= 0 ? ref.slice(slash + 1) : ref;
  let tag = "";
  const colon = rest.lastIndexOf(":");
  if (colon >= 0) {
    tag = rest.slice(colon);
    rest = rest.slice(0, colon);
  }
  return { prefix, name: rest, tag };
}

function imageHTML(image) {
  if (!image) return '<span class="muted">—</span>';
  const { prefix, name, tag } = splitImage(image);
  return `<div class="img-wrap" title="${esc(image)}">` +
    (prefix ? `<span class="img-prefix">${esc(prefix)}</span>` : "") +
    `<span class="img-name">${esc(name)}${tag ? `<span class="tag">${esc(tag)}</span>` : ""}</span>` +
    `</div>`;
}

function fmtPort(p) {
  return (p.host ? `${p.host}→${p.container}` : `${p.container}`) + `/${p.proto || "tcp"}`;
}

const PORTS_SHOWN = 3;

function portsHTML(ports) {
  if (!Array.isArray(ports) || ports.length === 0) return '<span class="muted">—</span>';
  const sorted = [...ports].sort((a, b) => (a.host || a.container) - (b.host || b.container));
  const all = sorted.map(fmtPort);
  const shown = all.slice(0, PORTS_SHOWN).map(esc).join(" ");
  const extra = all.length - PORTS_SHOWN;
  const title = esc(all.join("  "));
  return `<span title="${title}">${shown}${extra > 0 ? ` <span class="more">+${extra}</span>` : ""}</span>`;
}

function freshnessCell(s) {
  switch (s.freshness) {
    case "outdated":
      return `<span class="badge b-peach" title="A newer image is available for this tag.&#10;running: ${esc(s.image_digest || "?")}&#10;latest:  ${esc(s.latest_digest || "?")}">outdated</span>`;
    case "current":
      return '<span class="badge b-blue">up to date</span>';
    default:
      return '<span class="muted" title="Freshness not tracked — no image digest to compare (e.g. VMs/LXCs, or an image without a resolvable tag).">—</span>';
  }
}

// ------------------------------------------------------------ filtering ----

function matchesFilters(s, host) {
  if (state.removedOnly && s.state !== "removed") return false;
  if (!state.removedOnly && !state.showRemoved && s.state === "removed") return false;
  for (const key of state.chips) {
    const def = CHIP_DEFS.find((c) => c.key === key);
    if (def && !def.test(s)) return false;
  }
  const q = state.q.trim().toLowerCase();
  if (!q) return true;
  const hay = [s.name, s.external_id, s.image, s.kind, s.state, s.health, s.freshness,
    host.hostname, host.agent, host.platform, host.condition];
  if (s.labels && typeof s.labels === "object") {
    for (const [k, v] of Object.entries(s.labels)) hay.push(k, v);
  }
  return hay.some((x) => String(x ?? "").toLowerCase().includes(q));
}

function filterActive() {
  return state.q.trim() !== "" || state.chips.size > 0 || state.showRemoved || state.removedOnly;
}

// counts over the full dataset (independent of the active filter)
function computeCounts() {
  const c = {
    hosts: 0, staleHosts: 0, offlineHosts: 0, warningHosts: 0, criticalHosts: 0,
    services: 0, running: 0, unhealthy: 0, stopped: 0, outdated: 0, stale: 0, removed: 0,
  };
  for (const h of state.data.services?.hosts || []) {
    c.hosts++;
    if (h.status === "stale" && h.agent_status !== "stale" && h.agent_status !== "offline") c.staleHosts++;
    if (h.status === "offline" && h.agent_status !== "offline") c.offlineHosts++;
    if (h.status === "ok" && h.condition === "warning") c.warningHosts++;
    if (h.status === "ok" && h.condition === "critical") c.criticalHosts++;
    for (const s of h.services || []) {
      if (s.state === "removed") { c.removed++; continue; }
      c.services++;
      if (s.state === "running") c.running++;
      if (s.health === "unhealthy") c.unhealthy++;
      if (STOPPED_STATES.has(s.state)) c.stopped++;
      if (s.health === "stale") c.stale++;
      if (s.freshness === "outdated") c.outdated++;
    }
  }
  return c;
}

// ----------------------------------------------------------- attention ----

// The attention queue deliberately groups evidence by the next useful
// investigation step. It is not another collection of filter chips: it gives
// the dashboard an operational starting point, then routes into the catalogue
// or affected-agent list when the operator chooses to investigate.
function attentionItems() {
  const c = computeCounts();
  const agents = state.data.agents?.agents || [];
  const offline = agents.filter((a) => a.status === "offline").length;
  const staleAgents = agents.filter((a) => a.status === "stale").length;
  const item = (key, level, count, title, detail, action) => ({ key, level, count, title, detail, action });
  return [
    item("offline-agents", "critical", offline, "Offline agent", "Trove is no longer receiving reports from this source.", "Review agents"),
    item("offline-hosts", "critical", c.offlineHosts, "Offline host", "This host has missed its own reporting window and its inventory is stale.", "Review hosts"),
    item("critical-hosts", "critical", c.criticalHosts, "Critical host condition", "The platform reports that this host needs attention.", "Review hosts"),
    item("unhealthy", "critical", c.unhealthy, "Unhealthy service", "A platform health check or readiness signal is failing.", "Show services"),
    item("stale-agents", "warning", staleAgents, "Stale agent", "This source has missed its expected reporting interval.", "Review agents"),
    item("stale-hosts", "warning", c.staleHosts, "Stale host", "This host has stopped reporting even if another host from the same agent is still active.", "Review hosts"),
    item("warning-hosts", "warning", c.warningHosts, "Host condition warning", "The platform reports a degraded host condition.", "Review hosts"),
    item("stopped", "info", c.stopped, "Stopped workload", "A discovered workload is not running. It may be intentional.", "Show services"),
    item("removed", "info", c.removed, "Recently disappeared service", "A service is absent from its latest full-state report.", "Show services"),
    item("outdated", "info", c.outdated, "Outdated image", "The running image digest is behind the current tag.", "Show services"),
  ].filter((i) => i.count > 0);
}

function renderAttention() {
  const el = $("attention");
  const items = attentionItems();
  if (items.length === 0) {
    el.innerHTML = `<div class="attention-healthy">
      <span class="attention-icon" aria-hidden="true">✓</span>
      <div><strong>Nothing needs attention right now.</strong><span>All reporting agents and hosts are online, and discovered services have no current health, state, or image-freshness warnings.</span></div>
    </div>`;
    return;
  }
  el.innerHTML = items.map((i) => `<button type="button" class="attention-card attention-${i.level}" data-attention="${i.key}">
    <span class="attention-count">${i.count}</span>
    <span class="attention-copy"><strong>${esc(i.title)}${i.count === 1 ? "" : "s"}</strong><span>${esc(i.detail)}</span></span>
    <span class="attention-action">${esc(i.action)} <span aria-hidden="true">→</span></span>
  </button>`).join("");
}

function showAttention(key) {
  clearFilters();
  if (key === "offline-agents" || key === "stale-agents") {
    $("infrastructure-title").scrollIntoView({ behavior: "smooth", block: "start" });
    focusInvestigationTarget("infrastructure-title");
    return;
  }
  if (["offline-hosts", "stale-hosts", "critical-hosts", "warning-hosts"].includes(key)) {
    $("inventory-title").scrollIntoView({ behavior: "smooth", block: "start" });
    focusInvestigationTarget("inventory-title");
    return;
  }
  if (key === "removed") state.removedOnly = true;
  else state.chips.add(key);
  render();
  $("inventory-title").scrollIntoView({ behavior: "smooth", block: "start" });
  focusInvestigationTarget("inventory-title");
}

// Attention cards are replaced by render(), so keyboard focus cannot remain on
// the button that invoked an investigation. Move it to the visible destination
// instead of leaving it on document.body.
function focusInvestigationTarget(id) {
  requestAnimationFrame(() => $(id)?.focus({ preventScroll: true }));
}

// ------------------------------------------------------------- summary ----

// The summary is the at-a-glance answer: a health verdict + a read-only
// overview. Filtering lives in the chip row below, so these are not clickable
// (no duplicate controls).
function renderSummary() {
  const c = computeCounts();
  const agentList = state.data.agents?.agents || [];
  const agents = agentList.length;
  const items = attentionItems();
  const critical = items.some((i) => i.level === "critical");
  const warning = items.some((i) => i.level === "warning");
  const cls = critical ? "crit" : (warning ? "warn" : "ok");
  const text = critical || warning
    ? `${items.filter((i) => i.level !== "info").length} area${items.filter((i) => i.level !== "info").length === 1 ? "" : "s"} to review`
    : "Current state looks healthy";
  const overview =
    `${agents} agent${agents === 1 ? "" : "s"} · ` +
    `${c.hosts} host${c.hosts === 1 ? "" : "s"} · ` +
    `${c.services} service${c.services === 1 ? "" : "s"}`;

  $("summary").innerHTML =
    `<div class="verdict verdict-${cls}"><span class="vdot"></span>` +
    `<span class="vtext">${text}</span>` +
    `</div>` +
    `<span class="overview">${esc(overview)}</span>`;
}

function renderChips() {
  const c = computeCounts();
  const counts = { running: c.running, unhealthy: c.unhealthy, stopped: c.stopped, outdated: c.outdated, stale: c.stale };
  const chips = CHIP_DEFS.map((d) => {
    const active = state.chips.has(d.key) ? " active" : "";
    return `<button class="chip ${d.cls}${active}" data-chip="${d.key}" aria-pressed="${!!active}">
      ${d.key} <span class="n">${counts[d.key]}</span></button>`;
  });
  const remActive = state.showRemoved || state.removedOnly ? " active" : "";
  const remLabel = state.removedOnly ? "removed only" : "removed";
  chips.push(`<button class="chip c-gray${remActive}" data-chip="removed"
    aria-pressed="${state.showRemoved || state.removedOnly}" title="${state.removedOnly ? "showing only services no longer reported" : "include services no longer reported (kept 24h)"}">
    ${remLabel} <span class="n">${c.removed}</span></button>`);
  if (filterActive()) {
    chips.push(`<button class="chip chip-clear" data-clear="1" title="clear all filters (esc)">✕ clear</button>`);
  }
  $("chips").innerHTML = chips.join("");
}

// -------------------------------------------------------------- agents ----

function renderAgents() {
  const el = $("agents");
  const agents = state.data.agents?.agents || [];
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
      <div class="meta">${esc(a.platform || "—")}${a.version ? " · " + esc(agentVersionLabel(a.version)) : ""}</div>
      <div class="meta">last push: ${esc(relTime(a.last_seen_at))}</div>
    </div>`;
  }).join("");
}

// --------------------------------------------------------------- hosts ----

function serviceRow(host, s, isChild) {
  const key = keyOf(host, s);
  const removed = s.state === "removed";
  const cls = [
    removed ? "removed" : "",
    isChild ? "child" : "",
    key === state.drawerKey ? "open" : "",
  ].filter(Boolean).join(" ");
  const name = (isChild ? '<span class="tree">└─</span> ' : "") + esc(s.name || s.external_id);
  const kind = s.kind && s.kind !== "container" ? `<span class="kind">${esc(s.kind)}</span>` : "";
  return `<tr class="${cls}" tabindex="0" data-agent="${esc(host.agent)}"
      data-host="${esc(host.hostname)}" data-ext="${esc(s.external_id)}">
    <td class="svc" title="${esc(s.name || s.external_id)}"><span class="svc-name">${name}</span>${kind}</td>
    <td class="image">${imageHTML(s.image)}</td>
    <td class="badgecell">${badge(stateClass(s.state), s.state || "?")}</td>
    <td class="badgecell">${badge(HEALTH_CLASS[s.health] || "b-gray", s.health || "unknown")}</td>
    <td class="badgecell">${freshnessCell(s)}</td>
    <td class="ports">${portsHTML(s.ports)}</td>
    <td class="muted nowrap seen">${esc(relTime(s.last_seen_at))}</td>
  </tr>`;
}

function hostKey(h) {
  return `${h.agent}\u001f${h.hostname}`;
}

function renderHosts() {
  const el = $("hosts");
  const hosts = state.data.services?.hosts || [];
  if (hosts.length === 0) {
    el.innerHTML = '<div class="host"><div class="empty">No services reported yet.</div></div>';
    return;
  }

  const sections = [];
  for (const h of hosts) {
    const all = h.services || [];
    const total = all.filter((s) => state.removedOnly
      ? s.state === "removed"
      : state.showRemoved || s.state !== "removed").length;
    const visible = all.filter((s) => matchesFilters(s, h));
    if (filterActive() && visible.length === 0) continue; // host collapses out while filtering

    // Nest children under parents within the visible subset; orphans surface
    // at top level rather than vanishing.
    const ids = new Set(visible.map((s) => s.external_id));
    const childrenByParent = {};
    for (const s of visible) {
      if (s.parent_external_id && ids.has(s.parent_external_id)) {
        (childrenByParent[s.parent_external_id] ||= []).push(s);
      }
    }
    const topLevel = visible.filter((s) => !s.parent_external_id || !ids.has(s.parent_external_id));
    const rows = topLevel.map((s) => {
      let out = serviceRow(h, s, false);
      for (const k of childrenByParent[s.external_id] || []) out += serviceRow(h, k, true);
      return out;
    }).join("");

    // Per-host rollups reflect the host's real state, not the filter.
    const live = all.filter((s) => s.state !== "removed");
    const nUnhealthy = live.filter((s) => s.health === "unhealthy").length;
    const nOutdated = live.filter((s) => s.freshness === "outdated").length;
    const rollup =
      (nUnhealthy ? badge("b-red", `${nUnhealthy} unhealthy`, "mini") : "") +
      (nOutdated ? badge("b-peach", `${nOutdated} outdated`, "mini") : "");

    const st = h.status || "unknown";
    const condition = h.condition || "unknown";
    const conditionBadge = condition !== "unknown"
      ? badge(HOST_CONDITION_CLASS[condition] || "b-gray", `condition ${condition}`) : "";
    const collapsed = state.collapsed.has(hostKey(h));
    const countLabel = filterActive() && visible.length !== total
      ? `${visible.length}/${total} service(s)` : `${total} service(s)`;

    sections.push(`<div class="host${collapsed ? " collapsed" : ""}" data-hostkey="${esc(hostKey(h))}">
      <div class="host-head">
        <button type="button" class="host-toggle" data-host-toggle aria-expanded="${!collapsed}" aria-label="${collapsed ? "Expand" : "Collapse"} ${esc(h.hostname)} services">
          <span class="chev" aria-hidden="true">${collapsed ? "▸" : "▾"}</span>
          <span class="hostname">${esc(h.hostname)}</span>
        </button>
        <span class="sub">${esc(hostPlatformLine(h))}</span>
        <span class="sub">last report ${esc(relTime(h.last_seen_at))}</span>
        ${badge(AGENT_CLASS[st] || "b-gray", `reporting ${st}`)}
        ${conditionBadge}
        ${hostMetricsHTML(h.metrics)}
        <span class="rollup">${rollup}</span>
        <span class="count">${countLabel}</span>
        <button type="button" class="host-details" data-host-details data-hostkey="${esc(hostKey(h))}" aria-label="View ${esc(h.hostname)} host stats">
          View host stats <span aria-hidden="true">→</span>
        </button>
      </div>
      <p class="table-scroll-hint">Swipe or scroll horizontally to see all service details <span aria-hidden="true">→</span></p>
      <div class="host-body">
        <table>
          <thead><tr>
            <th class="w-service">Service</th><th class="w-image">Image</th><th class="w-state">State</th>
            <th class="w-health">Health</th><th class="w-fresh">Freshness</th>
            <th class="w-ports">Ports</th><th class="w-seen">Last seen</th>
          </tr></thead>
          <tbody>${rows || '<tr><td colspan="7" class="empty">nothing to show</td></tr>'}</tbody>
        </table>
      </div>
    </div>`);
  }

  el.innerHTML = sections.length > 0
    ? sections.join("")
    : `<div class="host"><div class="empty">${emptyMessage()}</div></div>`;
}

function emptyMessage() {
  const c = computeCounts();
  // If only chip filters are active and every active chip has zero matches,
  // the empty state is a positive: nothing is in that state.
  const activeChips = [...state.chips];
  if (!state.q.trim() && activeChips.length > 0) {
    const allZero = activeChips.every((k) => {
      const def = CHIP_DEFS.find((d) => d.key === k);
      return def && countsForChip(k, c) === 0;
    });
    if (allZero) {
      const label = activeChips.length === 1 ? activeChips[0] : "selected";
      return `No services are currently ${label}.`;
    }
  }
  return "No services match the current filter.";
}

function countsForChip(key, c) {
  return { running: c.running, unhealthy: c.unhealthy, stopped: c.stopped, outdated: c.outdated, stale: c.stale }[key] ?? 0;
}

// -------------------------------------------------------------- events ----

function renderEvents() {
  const el = $("events");
  const events = state.data.events?.events || [];
  if (events.length === 0) {
    el.innerHTML = '<div class="empty">No recent state changes.</div>';
    return;
  }
  el.innerHTML = events.slice(0, 40).map(eventRowHTML).join("");
}

const HEALTH_TEXT_CLASS = { healthy: "st-green", unhealthy: "st-red", stale: "st-yellow" };
const AGENT_TEXT_CLASS = { ok: "st-green", stale: "st-yellow", offline: "st-red" };

function eventRowHTML(e) {
  const from = e.from_state || "∅";
  let what;
  switch (e.kind) {
    case "agent":
      what = `<span class="kind">agent</span> <strong>${esc(e.agent)}</strong>
        &nbsp;<span class="st-gray">${esc(from)}</span> <span class="arrow">→</span>
        <span class="${AGENT_TEXT_CLASS[e.to_state] || "st-gray"}">${esc(e.to_state)}</span>`;
      break;
    case "host":
      what = `<span class="kind">host</span> <strong>${esc(e.hostname)}</strong>
        <span class="muted">@ ${esc(e.agent)}</span>
        &nbsp;<span class="st-gray">${esc(from)}</span> <span class="arrow">→</span>
        <span class="${AGENT_TEXT_CLASS[e.to_state] || "st-gray"}">${esc(e.to_state)}</span>`;
      break;
    case "health":
      what = `<strong>${esc(e.service)}</strong> <span class="muted">@ ${esc(e.hostname)}</span>
        <span class="kind">health</span>
        &nbsp;<span class="st-gray">${esc(from)}</span> <span class="arrow">→</span>
        <span class="${HEALTH_TEXT_CLASS[e.to_state] || "st-gray"}">${esc(e.to_state)}</span>`;
      break;
    default: // state
      what = `<strong>${esc(e.service)}</strong> <span class="muted">@ ${esc(e.hostname)}</span>
        &nbsp;<span class="st-gray">${esc(from)}</span> <span class="arrow">→</span>
        <span class="${stateTextClass(e.to_state)}">${esc(e.to_state)}</span>`;
  }
  return `<div class="event-row event-${eventTone(e)}">
    <span class="when nowrap">${esc(relTime(e.at))}</span>
    <span class="what">${what}</span>
  </div>`;
}

function eventTone(e) {
  if (e.kind === "agent" || e.kind === "host") {
    if (e.to_state === "offline") return "critical";
    if (e.to_state === "stale") return "warning";
    if (e.to_state === "ok") return "healthy";
  }
  if (e.kind === "health") {
    if (e.to_state === "unhealthy") return "critical";
    if (e.to_state === "stale") return "warning";
    if (e.to_state === "healthy") return "healthy";
  }
  if (e.kind === "state") {
    if (["dead", "failed"].includes(e.to_state)) return "critical";
    if (["exited", "removed", "restarting", "paused"].includes(e.to_state)) return "warning";
    if (e.to_state === "stopped") return "info";
    if (e.to_state === "running") return "healthy";
  }
  return "info";
}

// -------------------------------------------------------------- drawer ----

// findService looks the drawer target up in the *unfiltered* data so an open
// drawer survives the row being filtered out or soft-removed.
function findService(key) {
  for (const h of state.data.services?.hosts || []) {
    for (const s of h.services || []) {
      if (keyOf(h, s) === key) return { host: h, svc: s };
    }
  }
  return null;
}

function findHost(key) {
  return (state.data.services?.hosts || []).find((host) => hostKey(host) === key) || null;
}

function findAgent(name) {
  return (state.data.agents?.agents || []).find((agent) => agent.name === name) || null;
}

function drawerSection(title, body) {
  return body ? `<div class="d-sec">${title}</div>${body}` : "";
}

function hostMetaLabel(key) {
  return {
    "proxmox.version": "Proxmox version",
    "proxmox.release": "Proxmox release",
    "proxmox.repoid": "Proxmox repository ID",
    "docker.version": "Docker version",
    "docker.api_version": "Docker API version",
    "docker.os": "Docker OS",
    "docker.arch": "Docker architecture",
    "docker.os_name": "Operating system",
    "docker.kernel": "Kernel",
    "docker.host_metrics": "Host metrics source",
    "kubernetes.version": "Kubernetes version",
    "kubernetes.platform": "Kubernetes platform",
    "kubernetes.nodes": "Nodes",
    "kubernetes.ready_nodes": "Ready nodes",
    "kubernetes.metrics_api": "Metrics API",
    "linux.os": "Operating system",
    "linux.kernel": "Kernel",
    "linux.arch": "Architecture",
  }[key] || key;
}

function hostMetricsNoticeHTML(host) {
  const meta = host.meta && typeof host.meta === "object" ? host.meta : {};
  if ((host.platform === "kubernetes") && meta["kubernetes.metrics_api"] === "unavailable") {
    return `<div class="d-note">CPU and memory usage require the optional Kubernetes Metrics API. Node capacity and readiness are still reported.</div>`;
  }
  return "";
}

function missingHostMetricsHTML(host, agent) {
  const isProxmox = host.platform === "proxmox" || host.platform === "pve";
  const version = agent?.version ? ` (${esc(agent.version)})` : "";
  let detail;
  if (isProxmox && (host.condition || "unknown") === "unknown") {
    detail = `The connected Proxmox agent${version} appears to be using the older host report. ` +
      "Update and restart the Proxmox agent from the same Trove build as the server, then wait for its next report.";
  } else if (isProxmox) {
    detail = "The latest report did not contain a node resource snapshot. Check the Proxmox agent logs for node status API errors or permission problems.";
  } else if (host.platform === "kubernetes") {
    detail = "Apply the current read-only node RBAC. CPU and memory usage also require metrics-server or another metrics.k8s.io provider.";
  } else if (host.platform === "docker") {
    detail = "Live Docker host usage is available when the agent runs beside the daemon through its local Unix socket. Remote Docker APIs expose capacity but not host usage.";
  } else if (host.platform === "local") {
    detail = "The Linux agent could not read the aggregate procfs metrics for this host. Check its logs and /proc access.";
  } else {
    detail = "This platform did not include CPU, load, memory, disk, or uptime in its latest report.";
  }
  return `<div class="d-empty"><strong>No host metrics reported</strong><span>${detail}</span></div>`;
}

function renderHostDrawer(el, host) {
  const metrics = hostMetricRows(host.metrics);
  const agent = findAgent(host.agent);
  const meta = host.meta && typeof host.meta === "object"
    ? Object.entries(host.meta)
      .filter(([key]) => key !== "platform")
      .sort(([a], [b]) => a.localeCompare(b)) : [];
  const live = (host.services || []).filter((s) => s.state !== "removed");
  const inventory = [
    ["Services", String(live.length)],
    ["Running", String(live.filter((s) => s.state === "running").length)],
    ["Stopped", String(live.filter((s) => STOPPED_STATES.has(s.state)).length)],
    ["Unhealthy", String(live.filter((s) => s.health === "unhealthy").length)],
    ["Outdated", String(live.filter((s) => s.freshness === "outdated").length)],
  ];
  const metaValue = (value) => typeof value === "object" ? JSON.stringify(value) : String(value);
  const kv = (rows, cls = "") => `<div class="kv${cls ? ` ${cls}` : ""}">${rows.map(([k, v]) =>
    `<span class="k">${esc(k)}</span><span class="v">${esc(v)}</span>`).join("")}</div>`;
  const status = host.status || "unknown";
  const condition = host.condition || "unknown";

  el.innerHTML = `
    <div class="d-head">
      <span class="d-name">${esc(host.hostname)}</span>
      <span class="kind">host</span>
      <button class="d-close" aria-label="Close host stats" title="close (esc)">✕</button>
    </div>
    <div class="d-badges">
      ${badge(AGENT_CLASS[status] || "b-gray", `reporting ${status}`)}
      ${badge(HOST_CONDITION_CLASS[condition] || "b-gray", `condition ${condition}`)}
    </div>

    ${drawerSection("Host resources", metrics.length ? kv(metrics, "metrics") + hostMetricsNoticeHTML(host) : missingHostMetricsHTML(host, agent))}
    ${drawerSection("Platform details", meta.length ? kv(meta.map(([k, v]) => [hostMetaLabel(k), metaValue(v)])) : "")}
    ${drawerSection("Inventory", kv(inventory))}

    <div class="d-sec">Reporting</div>
    <div class="kv">
      <span class="k">last report</span><span class="v">${esc(absTime(host.last_seen_at))} (${esc(relTime(host.last_seen_at))})</span>
      <span class="k">agent</span><span class="v">${esc(host.agent)}</span>
      <span class="k">agent version</span><span class="v">${esc(agent?.version || "not reported")}</span>
      <span class="k">platform</span><span class="v">${esc(host.platform || "—")}</span>
    </div>
  `;
  el.hidden = false;
}

function renderDrawer() {
  const el = $("drawer");
  if (state.hostDrawerKey) {
    const host = findHost(state.hostDrawerKey);
    if (host) {
      renderHostDrawer(el, host);
      return;
    }
    state.hostDrawerKey = null;
  }
  if (!state.drawerKey) {
    el.hidden = true;
    el.innerHTML = "";
    return;
  }
  const found = findService(state.drawerKey);
  if (!found) { // service pruned entirely; nothing left to show
    state.drawerKey = null;
    el.hidden = true;
    el.innerHTML = "";
    return;
  }
  const { host, svc: s } = found;

  const siblings = host.services || [];
  const children = siblings.filter((x) => x.parent_external_id === s.external_id);
  const parent = s.parent_external_id
    ? siblings.find((x) => x.external_id === s.parent_external_id) : null;

  const events = (state.data.events?.events || [])
    .filter((e) => e.service_id === s.id)
    .slice(0, 12);

  const rawLabels = s.labels && typeof s.labels === "object" ? s.labels : {};
  const metricRows = isProxmoxService(host, s) ? proxmoxMetricRows(rawLabels) : [];
  const labels = Object.entries(rawLabels)
    .filter(([k]) => !PROXMOX_METRIC_KEYS.has(k))
    .sort(([a], [b]) => a.localeCompare(b));

  const ports = Array.isArray(s.ports)
    ? [...s.ports].sort((a, b) => (a.host || a.container) - (b.host || b.container)) : [];

  el.innerHTML = `
    <div class="d-head">
      <span class="d-name">${esc(s.name || s.external_id)}</span>
      <span class="kind">${esc(s.kind || "")}</span>
      <button class="d-close" aria-label="Close details" title="close (esc)">✕</button>
    </div>
    <div class="d-badges">
      ${badge(stateClass(s.state), s.state || "?")}
      ${badge(HEALTH_CLASS[s.health] || "b-gray", s.health || "unknown")}
      ${s.freshness === "outdated" ? badge("b-peach", "outdated")
        : s.freshness === "current" ? badge("b-blue", "up to date") : ""}
    </div>
    ${s.health_detail
      ? `<div class="d-detail ${s.health === "unhealthy" ? "bad" : ""}"><span class="d-detail-label">${s.health === "unhealthy" ? "Why" : "Detail"}</span> ${esc(s.health_detail)}</div>` : ""}
    <div class="d-mono">
      <span class="lbl">host</span> ${esc(host.hostname)} · <span class="lbl">agent</span> ${esc(host.agent)} (${esc(host.platform || "—")})
    </div>
    ${parent ? `<div class="d-mono"><span class="lbl">part of</span> ${esc(parent.name)} <span class="kind">${esc(parent.kind)}</span></div>` : ""}

    ${drawerSection("Image", s.image ? `
      <div class="d-mono">${esc(s.image)}</div>
      ${s.image_digest ? `<div class="d-mono"><span class="lbl">running</span> ${esc(s.image_digest)}</div>` : ""}
      ${s.latest_digest ? `<div class="d-mono"><span class="lbl">latest&nbsp;</span> ${esc(s.latest_digest)}</div>` : ""}` : "")}

    ${drawerSection("Ports", ports.length ? `<div class="pchips">${ports.map((p) => `<span class="pchip">${esc(fmtPort(p))}</span>`).join("")}</div>` : "")}

    ${drawerSection("Metrics", metricRows.length ? `<div class="kv metrics">${metricRows.map(([k, v]) =>
      `<span class="k">${esc(k)}</span><span class="v">${esc(v)}</span>`).join("")}</div>` : "")}

    ${drawerSection(`Instances (${children.length})`, children.length ? `<div class="d-kids">${children.map((k) => `
      <div class="krow">${badge(HEALTH_CLASS[k.health] || "b-gray", k.health, "mini")} ${esc(k.name)}
        <span class="muted">${esc(k.state)}</span></div>`).join("")}</div>` : "")}

    ${drawerSection("Labels", labels.length ? `<div class="kv">${labels.map(([k, v]) =>
      `<span class="k">${esc(k)}</span><span class="v">${esc(v)}</span>`).join("")}</div>` : "")}

    <div class="d-sec">Seen</div>
    <div class="kv">
      <span class="k">first seen</span><span class="v">${esc(absTime(s.first_seen_at))} (${esc(relTime(s.first_seen_at))})</span>
      <span class="k">last seen</span><span class="v">${esc(absTime(s.last_seen_at))} (${esc(relTime(s.last_seen_at))})</span>
      <span class="k">external id</span><span class="v">${esc(s.external_id)}</span>
    </div>

    ${drawerSection("Recent events", events.length ? `<div class="d-events">${events.map(eventRowHTML).join("")}</div>` : "")}
  `;
  el.hidden = false;
}

// ------------------------------------------------------ keyboard cursor ----

function visibleRows() {
  return Array.from(document.querySelectorAll("#hosts tr[data-ext]"));
}

function rowKey(tr) {
  return `${tr.dataset.agent}\u001f${tr.dataset.host}\u001f${tr.dataset.ext}`;
}

function applyCursor() {
  for (const tr of visibleRows()) {
    tr.classList.toggle("cursor", rowKey(tr) === state.cursorKey);
  }
}

function moveCursor(delta) {
  const rows = visibleRows();
  if (rows.length === 0) return;
  let idx = rows.findIndex((tr) => rowKey(tr) === state.cursorKey);
  idx = idx === -1 ? (delta > 0 ? 0 : rows.length - 1)
    : Math.min(rows.length - 1, Math.max(0, idx + delta));
  state.cursorKey = rowKey(rows[idx]);
  applyCursor();
  rows[idx].scrollIntoView({ block: "nearest" });
}

function openDrawer(key) {
  state.hostDrawerKey = null;
  state.drawerKey = key;
  state.cursorKey = key;
  render();
  requestAnimationFrame(() => document.querySelector(".d-close")?.focus({ preventScroll: true }));
}

function openHostDrawer(key) {
  state.drawerKey = null;
  state.hostDrawerKey = key;
  render();
  requestAnimationFrame(() => document.querySelector(".d-close")?.focus({ preventScroll: true }));
}

function closeDrawer() {
  const serviceKey = state.drawerKey;
  const hostKeyToRestore = state.hostDrawerKey;
  state.drawerKey = null;
  state.hostDrawerKey = null;
  render();
  requestAnimationFrame(() => {
    if (hostKeyToRestore) {
      Array.from(document.querySelectorAll("[data-host-details]"))
        .find((button) => button.dataset.hostkey === hostKeyToRestore)?.focus({ preventScroll: true });
      return;
    }
    visibleRows().find((tr) => rowKey(tr) === serviceKey)?.focus({ preventScroll: true });
  });
}

// -------------------------------------------------------------- render ----

function render() {
  if (!state.data.services || !state.data.agents) return;
  renderAttention();
  renderSummary();
  renderChips();
  renderAgents();
  renderHosts();
  renderEvents();
  renderDrawer();
  applyCursor();
}

// ---------------------------------------------------------- poll loop ----

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
      fetchJSON("api/v1/events?limit=200"),
    ]);
    state.data = { services, agents, events };
    render();
    setStatus(true);
  } catch (e) {
    setStatus(false, e.message || "connection lost");
  }
}

// ---- user chip / sign-out ----

async function loadUserChip() {
  try {
    const res = await fetch("api/v1/me", { headers: { Accept: "application/json" } });
    if (!res.ok) return;
    const me = await res.json();
    const chip = $("user-chip");
    if (me.authenticated && me.via === "oidc" && me.email) {
      chip.innerHTML = "";
      const email = document.createElement("span");
      email.className = "user-email";
      email.textContent = me.email;
      email.title = me.email;
      const btn = document.createElement("button");
      btn.className = "sign-out";
      btn.textContent = "Sign out";
      btn.addEventListener("click", () => {
        // Use a real form submission instead of fetch so any IdP logout
        // redirect is followed as top-level browser navigation.
        const form = document.createElement("form");
        form.method = "POST";
        form.action = "oauth2/logout";
        form.hidden = true;
        document.body.appendChild(form);
        form.submit();
      });
      chip.appendChild(email);
      chip.appendChild(btn);
      chip.hidden = false;
    } else {
      chip.hidden = true;
    }
  } catch {
    // not configured or unreachable — hide the chip
    $("user-chip").hidden = true;
  }
}

// ------------------------------------------------------------- wiring ----

function toggleChip(key) {
  if (key === "removed") {
    if (state.removedOnly) state.removedOnly = false;
    else state.showRemoved = !state.showRemoved;
  } else if (state.chips.has(key)) {
    state.chips.delete(key);
  } else {
    state.chips.add(key);
  }
  render();
  requestAnimationFrame(() => document.querySelector(`[data-chip=\"${key}\"]`)?.focus({ preventScroll: true }));
}

function clearFilters() {
  state.q = "";
  $("q").value = "";
  state.chips.clear();
  state.showRemoved = false;
  state.removedOnly = false;
  render();
}

document.addEventListener("click", (e) => {
  if (e.target.closest("[data-clear]")) { clearFilters(); return; }

  const attention = e.target.closest("[data-attention]");
  if (attention) { showAttention(attention.dataset.attention); return; }

  const chip = e.target.closest("[data-chip]");
  if (chip) { toggleChip(chip.dataset.chip); return; }

  if (e.target.closest(".d-close")) { closeDrawer(); return; }
  if (e.target.closest("#drawer")) return; // clicks inside the drawer stay put

  const hostDetails = e.target.closest("[data-host-details]");
  if (hostDetails) { openHostDrawer(hostDetails.dataset.hostkey); return; }

  const hostToggle = e.target.closest("[data-host-toggle]");
  if (hostToggle) {
    const key = hostToggle.closest(".host").dataset.hostkey;
    if (state.collapsed.has(key)) state.collapsed.delete(key);
    else state.collapsed.add(key);
    render();
    return;
  }

  const tr = e.target.closest("#hosts tr[data-ext]");
  if (tr) { openDrawer(rowKey(tr)); return; }
});

document.addEventListener("keydown", (e) => {
  const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName);

  if (e.key === "/" && !typing) {
    e.preventDefault();
    $("q").focus();
    return;
  }
  if (e.key === "Escape") {
    if (typing) { e.target.blur(); return; }
    if (state.drawerKey || state.hostDrawerKey) { closeDrawer(); return; }
    if (filterActive()) clearFilters();
    return;
  }
  if (typing) return;

  if (e.key === "j" || e.key === "ArrowDown") { e.preventDefault(); moveCursor(1); return; }
  if (e.key === "k" || e.key === "ArrowUp") { e.preventDefault(); moveCursor(-1); return; }
  if (e.key === "Enter") {
    if (e.target.closest?.(".host-toggle, .host-details")) return;
    const focused = document.activeElement?.closest?.("#hosts tr[data-ext]");
    if (focused) { openDrawer(rowKey(focused)); return; }
    if (state.cursorKey) openDrawer(state.cursorKey);
  }
});

let qTimer;
$("q").addEventListener("input", (e) => {
  clearTimeout(qTimer);
  qTimer = setTimeout(() => {
    state.q = e.target.value;
    render();
  }, 120);
});

refresh();
setInterval(refresh, POLL_MS);
loadUserChip();
