#!/usr/bin/env python3
"""Reproducible Trove scale benchmark.

Exercises the real HTTP ingest and read APIs against a local server. The
benchmark deliberately leaves the server's one-connection SQLite policy alone.
Run from the repository root: python3 scripts/scale_benchmark.py.
"""
from __future__ import annotations

import json
import os
import shutil
import signal
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / ".scale-benchmark"
DB = WORK / "trove.db"
BASE_URL = "http://127.0.0.1:18080"
AGENTS = 50
SERVICES_PER_AGENT = 200
TARGET_EVENTS = 100_000
TOKEN = "scale-benchmark-token-change-me"


def percentile(values, p):
    values = sorted(values)
    if not values:
        return None
    idx = min(len(values) - 1, max(0, round((p / 100) * (len(values) - 1))))
    return values[idx]


def request(method, path, body=None, token=None):
    data = None if body is None else json.dumps(body, separators=(",", ":")).encode()
    req = urllib.request.Request(BASE_URL + path, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    started = time.perf_counter()
    with urllib.request.urlopen(req, timeout=120) as response:
        payload = response.read()
        return (time.perf_counter() - started) * 1000, response.status, payload


def proc_stats(pid):
    stat = Path(f"/proc/{pid}/stat")
    status = Path(f"/proc/{pid}/status")
    out = {}
    if stat.exists():
        fields = stat.read_text().split()
        out["cpu_ticks"] = int(fields[13]) + int(fields[14])
    if status.exists():
        for line in status.read_text().splitlines():
            if line.startswith("VmRSS:"):
                out["rss_kib"] = int(line.split()[1])
    return out


def db_sizes():
    return {p.name: p.stat().st_size for p in WORK.glob("trove.db*") if p.exists()}


def service_report(agent, round_no):
    services = []
    for i in range(SERVICES_PER_AGENT):
        # Every update changes state, producing one retained state event per service.
        state = "running" if round_no % 2 else "paused"
        services.append({
            "external_id": f"{agent}-{i:04d}",
            "name": f"service-{agent}-{i:04d}",
            "kind": "container",
            "image": "registry.example.invalid/scale/service:1",
            "state": state,
            "health": "healthy",
        })
    return {
        "agent": {"name": agent, "platform": "docker", "version": "scale", "interval_seconds": 3600},
        "host": {"hostname": f"host-{agent}", "condition": "normal"},
        "services": services,
    }


def wait_ready(proc):
    deadline = time.time() + 30
    while time.time() < deadline:
        try:
            _, status, _ = request("GET", "/healthz")
            if status == 200:
                return
        except Exception:
            pass
        time.sleep(0.1)
    raise RuntimeError(proc.stderr.read() if proc.poll() is not None else "server did not become ready")


def main():
    WORK.mkdir(exist_ok=True)
    for p in WORK.glob("trove.db*"):
        p.unlink()
    env = os.environ.copy()
    env.update({"TROVE_ADDR": ":18080", "TROVE_DB": str(DB), "TROVE_HOST_RETENTION": "8760h"})
    server_binary = os.environ.get("TROVE_SERVER_BINARY")
    if server_binary:
        binary = Path(server_binary).expanduser().resolve()
    elif shutil.which("go"):
        binary = WORK / "trove-server"
        build = subprocess.run(["go", "build", "-o", str(binary), "./cmd/trove-server"], cwd=ROOT, text=True, capture_output=True)
        if build.returncode:
            raise RuntimeError(build.stderr)
    else:
        raise RuntimeError("Go is not installed; set TROVE_SERVER_BINARY to a built trove-server")
    server = subprocess.Popen([str(binary), "serve"], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True)
    try:
        wait_ready(server)
        agents = []
        # Use the production CLI path to mint tokens; no direct SQL setup.
        for i in range(AGENTS):
            name = f"scale-{i:03d}"
            result = subprocess.run([str(binary), "agent", "create", name], cwd=ROOT, env=env, text=True, capture_output=True, check=True)
            token = next(line.strip() for line in result.stdout.splitlines() if line.strip().startswith("trove_"))
            agents.append((name, token))

        ingest_ms = []
        cpu_start = proc_stats(server.pid)
        for round_no in range(12):
            for name, token in agents:
                elapsed, status, _ = request("POST", "/api/v1/report", service_report(name, round_no), token)
                if status != 200:
                    raise RuntimeError(f"ingest failed: {status}")
                ingest_ms.append(elapsed)
        cpu_end = proc_stats(server.pid)

        api = {}
        for path in ["/api/v1/agents", "/api/v1/services?limit=500", "/api/v1/events?limit=500"]:
            samples = []
            sizes = []
            for _ in range(5):
                elapsed, status, payload = request("GET", path)
                if status != 200:
                    raise RuntimeError(f"GET {path}: {status}")
                samples.append(elapsed)
                sizes.append(len(payload))
            api[path] = {"p50_ms": percentile(samples, 50), "p95_ms": percentile(samples, 95), "response_bytes": statistics.median(sizes)}

        # Stop before inspecting SQLite so WAL and main-file sizes are stable.
        server.send_signal(signal.SIGTERM)
        server.wait(timeout=15)
        restart_started = time.perf_counter()
        server = subprocess.Popen([str(binary), "serve"], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True)
        wait_ready(server)
        restart_ms = (time.perf_counter() - restart_started) * 1000
        server.send_signal(signal.SIGTERM)
        server.wait(timeout=15)
        sizes = db_sizes()
        import sqlite3
        con = sqlite3.connect(DB)
        counts = {table: con.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0] for table in ["agents", "hosts", "services", "events"]}
        integrity = con.execute("PRAGMA integrity_check").fetchone()[0]
        before_maintenance = time.perf_counter()
        con.execute("DELETE FROM events WHERE at < ?", (int(time.time()) + 1,))
        con.commit()
        maintenance_ms = (time.perf_counter() - before_maintenance) * 1000
        con.close()

        result = {
            "benchmark": {"agents": AGENTS, "services": AGENTS * SERVICES_PER_AGENT, "retained_events_target": TARGET_EVENTS, "rounds": 12},
            "counts": counts,
            "integrity": integrity,
            "ingest": {"requests": len(ingest_ms), "p50_ms": percentile(ingest_ms, 50), "p95_ms": percentile(ingest_ms, 95), "max_ms": max(ingest_ms), "cpu_ticks": cpu_end.get("cpu_ticks", 0) - cpu_start.get("cpu_ticks", 0), "rss_kib": cpu_end.get("rss_kib")},
            "api": api,
            "sqlite_files_bytes": sizes,
            "restart_ms": round(restart_ms, 2),
            "maintenance_delete_all_events_ms": round(maintenance_ms, 2),
            "notes": ["Profiled the unmodified server binary before any optimization.", "The benchmark does not alter Store.Open's intentional SetMaxOpenConns(1).", "Maintenance timing uses the same DELETE predicate on a stopped benchmark DB; production Prune also handles removed services and hosts."],
        }
        print(json.dumps(result, indent=2, sort_keys=True))
    finally:
        if server.poll() is None:
            server.terminate()
            try: server.wait(timeout=5)
            except subprocess.TimeoutExpired: server.kill()


if __name__ == "__main__":
    main()
