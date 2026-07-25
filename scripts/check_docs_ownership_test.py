#!/usr/bin/env python3

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from scripts.check_docs_ownership import check


class DocumentationOwnershipCheckTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / "docs").mkdir()
        (self.root / "README.md").write_text(
            """# Trove

[Documentation](docs/README.md)
[Authentication](docs/authentication.md)
[Upgrades](docs/upgrades.md)
[Release security](docs/release-security.md)

## Configuration reference

#### Dashboard authentication (OIDC)

Configuration belongs here.

## Upgrades & backup

See the canonical guide.
""",
            encoding="utf-8",
        )
        (self.root / "docs/README.md").write_text(
            """Repository documentation is versioned with the code.

Root README
Repository docs
Wiki
Non-versioned walkthroughs
""",
            encoding="utf-8",
        )
        (self.root / "docs/authentication.md").write_text(
            "[Configuration](../README.md#configuration-reference)\n",
            encoding="utf-8",
        )
        (self.root / "docs/upgrades.md").write_text(
            "## Rolling back\n\nRestore the verified backup.\n",
            encoding="utf-8",
        )
        (self.root / "docs/release-security.md").write_text(
            "## Verify a container image\n",
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.temp.cleanup()

    def test_valid_boundary_passes(self) -> None:
        self.assertEqual(check(self.root), [])

    def test_authentication_variable_table_fails(self) -> None:
        path = self.root / "docs/authentication.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "\n| `TROVE_OIDC_ISSUER` | duplicated configuration |\n",
            encoding="utf-8",
        )
        self.assertTrue(any("environment-variable tables" in error for error in check(self.root)))

    def test_readme_upgrade_commands_fail(self) -> None:
        path = self.root / "README.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "\n```sh\ndocker compose pull\ndocker compose up -d\n```\n",
            encoding="utf-8",
        )
        self.assertTrue(any("command blocks" in error for error in check(self.root)))

    def test_duplicated_operational_example_fails(self) -> None:
        block = "```sh\nfirst command\nsecond command\n```\n"
        readme = self.root / "README.md"
        readme.write_text(
            readme.read_text(encoding="utf-8").replace(
                "## Configuration reference", f"{block}\n## Configuration reference"
            ),
            encoding="utf-8",
        )
        authentication = self.root / "docs/authentication.md"
        authentication.write_text(
            authentication.read_text(encoding="utf-8") + f"\n{block}",
            encoding="utf-8",
        )
        self.assertTrue(
            any("duplicates an operational command block" in error for error in check(self.root))
        )


if __name__ == "__main__":
    unittest.main()
