#!/usr/bin/env python3
"""Verify every release-please-managed version marker matches the manifest."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / ".release-please-manifest.json"
CONFIG = ROOT / "release-please-config.json"
MARKER = "x-release-please-version"
EXPECTED_FILES = {
    "README.md",
    "ROADMAP.md",
    "docs/agents/local.md",
    "docs/upgrades.md",
    "deploy/kubernetes/trove-agent.yaml",
}
VERSION_RE = re.compile(r"(?<![0-9])v?(\d+\.\d+\.\d+)(?![0-9])")


def release_version() -> str:
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    version = manifest.get(".")
    if not isinstance(version, str) or not VERSION_RE.fullmatch(version):
        raise ValueError(".release-please-manifest.json must contain a SemVer version for '.'")
    return version


def configured_files() -> set[str]:
    config = json.loads(CONFIG.read_text(encoding="utf-8"))
    package = config.get("packages", {}).get(".", {})
    files: set[str] = set()
    for item in package.get("extra-files", []):
        if isinstance(item, str):
            files.add(item)
        elif isinstance(item, dict) and isinstance(item.get("path"), str):
            files.add(item["path"])
        else:
            raise ValueError(f"invalid extra-files entry: {item!r}")
    return files


def main() -> int:
    version = release_version()
    errors: list[str] = []

    configured = configured_files()
    if configured != EXPECTED_FILES:
        errors.append(
            "release-please extra-files do not match the release surface: "
            f"got {sorted(configured)}, want {sorted(EXPECTED_FILES)}"
        )

    for relative in sorted(EXPECTED_FILES):
        path = ROOT / relative
        marker_lines = [
            (line_number, line)
            for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1)
            if MARKER in line
        ]
        if not marker_lines:
            errors.append(f"{relative}: missing {MARKER} annotation")
            continue
        for line_number, line in marker_lines:
            versions = VERSION_RE.findall(line)
            if version not in versions:
                errors.append(
                    f"{relative}:{line_number}: annotated version does not match manifest "
                    f"{version!r}: {line.strip()}"
                )

    if errors:
        print("release surface check failed:", file=sys.stderr)
        print("\n".join(f"- {error}" for error in errors), file=sys.stderr)
        return 1

    print(f"release surface matches v{version} across {len(EXPECTED_FILES)} files")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
