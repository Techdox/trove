#!/usr/bin/env python3
"""Reproducible release gate for the server/agent compatibility contract."""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MATRIX = ROOT / "docs" / "compatibility-matrix.json"


def fail(message: str) -> None:
    raise SystemExit(f"compatibility gate: FAIL: {message}")


def main() -> int:
    matrix = json.loads(MATRIX.read_text())
    previous = matrix["previous_release"]
    platforms = matrix["platforms"]
    expected = {"docker", "kubernetes", "proxmox", "local"}
    if set(platforms) != expected:
        fail(f"platform set is {sorted(platforms)}, expected {sorted(expected)}")

    if matrix["upgrade_order"] != ["server", "agents"]:
        fail("upgrade order is not server first")
    skew = matrix["supported_skew"]
    if skew.get("current_server_previous_agent") != "supported":
        fail("immediately-previous agent is not marked supported")
    for key in ("previous_server_current_agent", "current_server_older_than_previous_agent",
                "current_server_newer_than_previous_agent"):
        if skew.get(key) != "unsupported":
            fail(f"unsafe skew {key} is not explicitly unsupported")

    for platform, entry in platforms.items():
        report_path = ROOT / entry["previous_report"]
        if not report_path.is_file():
            fail(f"missing frozen {platform} report: {entry['previous_report']}")
        report = json.loads(report_path.read_text())
        agent = report.get("agent", {})
        if agent.get("version") != f"v{previous}":
            fail(f"{platform} fixture agent version is {agent.get('version')!r}, expected v{previous!r}")
        if agent.get("platform") != platform:
            fail(f"{platform} fixture identifies platform {agent.get('platform')!r}")
        if not report.get("host", {}).get("hostname"):
            fail(f"{platform} fixture has no host identity")
        if not isinstance(report.get("services"), list):
            fail(f"{platform} fixture has no services array")
        artifact = entry.get("agent_image", entry.get("agent_archive", ""))
        if "{version}" not in artifact:
            fail(f"{platform} has no versioned previous-release artifact template")

    messages = matrix["failure_messages"]
    for key in ("upgrade_order", "rollback"):
        if not messages.get(key):
            fail(f"missing operator failure message: {key}")

    print(f"compatibility gate: PASS ({len(platforms)} platforms, server {matrix['current_release']} accepts agent v{previous} fixtures)")
    print("compatibility gate: PASS (unsafe skew and rollback contract are explicit)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
