#!/usr/bin/env python3
"""Check the published v1 wire schemas for additive evolution and DTO drift."""
import json, re, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / "schemas" / "v1"
BASELINE = SCHEMA / "baseline.json"


def load(name):
    return json.loads((SCHEMA / name).read_text())


def props(schema):
    return set(schema.get("properties", {}))


def compare_schema(old, current, path, failures):
    """Compare every nested schema object represented in the frozen baseline."""
    if not isinstance(old, dict):
        return
    if not isinstance(current, dict):
        failures.append(f"{path}: schema node was removed or changed shape")
        return

    old_type = old.get("type")
    new_type = current.get("type")
    if old_type != new_type:
        failures.append(f"{path}: changed type {old_type!r} -> {new_type!r}")

    old_props = old.get("properties", {})
    new_props = current.get("properties", {})
    missing = set(old_props) - set(new_props)
    if missing:
        failures.append(f"{path}: removed properties: {sorted(missing)}")
    newly_required = set(current.get("required", [])) - set(old.get("required", []))
    if newly_required:
        failures.append(f"{path}: newly required fields: {sorted(newly_required)}")
    for field in set(old_props) & set(new_props):
        compare_schema(old_props[field], new_props[field], f"{path}.properties.{field}", failures)

    old_defs = old.get("$defs", {})
    new_defs = current.get("$defs", {})
    missing_defs = set(old_defs) - set(new_defs)
    if missing_defs:
        failures.append(f"{path}: removed definitions: {sorted(missing_defs)}")
    for name in set(old_defs) & set(new_defs):
        compare_schema(old_defs[name], new_defs[name], f"{path}.$defs.{name}", failures)

    if "items" in old:
        if "items" not in current:
            failures.append(f"{path}: removed array item schema")
        else:
            compare_schema(old["items"], current["items"], f"{path}.items", failures)

    old_enum = set(old.get("enum", []))
    new_enum = set(current.get("enum", []))
    if old_enum and not old_enum.issubset(new_enum):
        failures.append(f"{path}: removed enum values: {sorted(old_enum - new_enum)}")


def check_additive():
    baseline = json.loads(BASELINE.read_text())
    failures = []
    for name, old in baseline.items():
        compare_schema(old, load(name), name, failures)
    return failures


def check_dto(source, type_name, schema_name, definition=None):
    text = (ROOT / source).read_text()
    match = re.search(r"type\s+" + re.escape(type_name) + r"\s+struct\s*\{(.*?)\n\}", text, re.S)
    if not match:
        return [f"could not find Go DTO {type_name}"]
    go_fields = set()
    for line in match.group(1).splitlines():
        m = re.search(r"^\s*\w+\s+[^`]+`json:\"([^,\"]+)", line)
        if m and m.group(1) != "-":
            go_fields.add(m.group(1))
    schema = load(schema_name)
    if definition:
        schema = schema["$defs"][definition]
    schema_fields = props(schema)
    missing = go_fields - schema_fields
    extra = schema_fields - go_fields
    failures = []
    if missing:
        failures.append(f"{schema_name}: DTO fields missing from schema: {sorted(missing)}")
    if extra:
        failures.append(f"{schema_name}: schema fields missing from DTO: {sorted(extra)}")
    return failures


def check_fixture(path, schema):
    value = json.loads(path.read_text())
    failures = []
    for field in schema.get("required", []):
        if field not in value:
            failures.append(f"{path.name}: missing required field {field}")
    return failures


def main():
    failures = check_additive()
    failures += check_dto("internal/server/read.go", "serviceDTO", "services.json", "service")
    failures += check_dto("internal/server/read.go", "hostGroupDTO", "services.json", "host")
    failures += check_dto("internal/server/read.go", "agentDTO", "agents.json", "agent")
    failures += check_dto("internal/server/read.go", "eventDTO", "events.json", "event")
    failures += check_dto("internal/server/install.go", "createAgentResponse", "agent-create.json")
    failures += check_dto("internal/server/install.go", "deleteAgentResponse", "agent-delete.json")
    report = load("report.json")
    for fixture in sorted((ROOT / "internal/server/testdata").glob("report-v*.json")):
        failures += check_fixture(fixture, report)
    if failures:
        print("API schema check failed:", *failures, sep="\n- ")
        return 1
    print("API schemas: additive baseline, DTO fields, and report fixtures passed")
    return 0

if __name__ == "__main__":
    sys.exit(main())
