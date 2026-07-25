#!/usr/bin/env python3
"""Keep install/configuration and operational documentation ownership distinct."""

from __future__ import annotations

import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
FENCE = re.compile(r"```[^\n]*\n(?P<body>.*?)```", re.DOTALL)


def read(root: Path, relative: str) -> str:
    path = root / relative
    if not path.is_file():
        raise ValueError(f"{relative}: canonical documentation file is missing")
    return path.read_text(encoding="utf-8")


def section(text: str, heading: str) -> str:
    lines = text.splitlines()
    try:
        start = lines.index(heading)
    except ValueError as exc:
        raise ValueError(f"missing canonical heading {heading!r}") from exc

    level = len(heading) - len(heading.lstrip("#"))
    end = len(lines)
    for index in range(start + 1, len(lines)):
        match = re.match(r"^(#{1,6})\s", lines[index])
        if match and len(match.group(1)) <= level:
            end = index
            break
    return "\n".join(lines[start:end])


def substantial_blocks(text: str) -> set[str]:
    blocks: set[str] = set()
    for match in FENCE.finditer(text):
        body = "\n".join(line.rstrip() for line in match.group("body").strip().splitlines())
        meaningful = [line for line in body.splitlines() if line.strip() and not line.lstrip().startswith("#")]
        if len(meaningful) >= 2:
            blocks.add(body)
    return blocks


def check(root: Path = ROOT) -> list[str]:
    errors: list[str] = []
    try:
        readme = read(root, "README.md")
        docs_index = read(root, "docs/README.md")
        authentication = read(root, "docs/authentication.md")
        upgrades = read(root, "docs/upgrades.md")
        release_security = read(root, "docs/release-security.md")
    except ValueError as exc:
        return [str(exc)]

    required_readme_links = {
        "documentation ownership": "(docs/README.md)",
        "authentication": "(docs/authentication.md)",
        "upgrades": "(docs/upgrades.md)",
        "release security": "(docs/release-security.md)",
    }
    for label, link in required_readme_links.items():
        if link not in readme:
            errors.append(f"README.md: missing canonical {label} link {link}")

    if "(../README.md#configuration-reference)" not in authentication:
        errors.append(
            "docs/authentication.md: must link to the README configuration reference"
        )
    if re.search(r"^\|\s*`TROVE_[A-Z0-9_]+`", authentication, re.MULTILINE):
        errors.append(
            "docs/authentication.md: environment-variable tables belong in README.md"
        )

    try:
        oidc = section(readme, "#### Dashboard authentication (OIDC)")
        upgrade_summary = section(readme, "## Upgrades & backup")
    except ValueError as exc:
        errors.append(f"README.md: {exc}")
    else:
        oidc_summary = oidc.split("Private registry / Docker Hub credentials", 1)[0]
        if "```" in oidc_summary:
            errors.append(
                "README.md: OIDC configuration summary must link to operational examples, not copy them"
            )
        if "```" in upgrade_summary:
            errors.append(
                "README.md: upgrade summary must link to docs/upgrades.md, not contain command blocks"
            )

    duplicate_blocks = substantial_blocks(readme) & (
        substantial_blocks(authentication) | substantial_blocks(upgrades)
    )
    for block in sorted(duplicate_blocks):
        first_line = block.splitlines()[0][:80]
        errors.append(
            f"README.md: duplicates an operational command block beginning {first_line!r}"
        )

    ownership_phrases = (
        "Repository documentation is versioned with the code",
        "Root README",
        "Repository docs",
        "Wiki",
        "Non-versioned walkthroughs",
    )
    for phrase in ownership_phrases:
        if phrase not in docs_index:
            errors.append(f"docs/README.md: ownership boundary is missing {phrase!r}")

    if "## Rolling back" not in upgrades:
        errors.append("docs/upgrades.md: canonical rollback procedure is missing")
    if "Verify a container image" not in release_security:
        errors.append("docs/release-security.md: canonical image verification is missing")

    return errors


def main() -> int:
    errors = check()
    if errors:
        print("documentation ownership check failed:", file=sys.stderr)
        print("\n".join(f"- {error}" for error in errors), file=sys.stderr)
        return 1

    print("documentation ownership boundary is intact")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
