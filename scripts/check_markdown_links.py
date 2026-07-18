#!/usr/bin/env python3
"""Fail when repository Markdown points at a missing local file."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[1]
SKIP_DIRS = {".git", ".agents", "node_modules", "vendor"}
SKIP_FILES = {"AGENTS.md"}
INLINE_LINK = re.compile(r"!?\[[^\]]*\]\((?P<target><[^>]+>|[^\s)]+)(?:\s+[^)]*)?\)")
REFERENCE_LINK = re.compile(r"^\s*\[[^\]]+\]:\s*(?P<target><[^>]+>|\S+)", re.MULTILINE)


def markdown_files() -> list[Path]:
    return sorted(
        path
        for path in ROOT.rglob("*.md")
        if path.name not in SKIP_FILES and not any(part in SKIP_DIRS for part in path.parts)
    )


def local_target(raw: str) -> str | None:
    target = raw.removeprefix("<").removesuffix(">")
    if target.startswith(("#", "//", "/")):
        return None
    parsed = urlsplit(target)
    if parsed.scheme or not parsed.path:
        return None
    return unquote(parsed.path)


def main() -> int:
    missing: list[tuple[Path, str]] = []
    files = markdown_files()
    for source in files:
        text = source.read_text(encoding="utf-8")
        matches = list(INLINE_LINK.finditer(text)) + list(REFERENCE_LINK.finditer(text))
        for match in matches:
            raw = match.group("target")
            target = local_target(raw)
            if target is None:
                continue
            resolved = (source.parent / target).resolve()
            if not resolved.exists():
                missing.append((source.relative_to(ROOT), raw))

    if missing:
        for source, target in sorted(missing):
            print(f"{source}: missing local Markdown target {target}", file=sys.stderr)
        return 1

    print(f"checked {len(files)} Markdown files; all local targets exist")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
