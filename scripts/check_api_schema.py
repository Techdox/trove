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


def check_additive():
    baseline = json.loads(BASELINE.read_text())
    failures = []
    for name, old in baseline.items():
        current = load(name)
        missing = props(old) - props(current)
        if missing:
            failures.append(f"{name}: removed properties: {sorted(missing)}")
        required_missing = set(old.get("required", [])) - set(current.get("required", []))
        if required_missing:
            failures.append(f"{name}: baseline required fields changed: {sorted(required_missing)}")
        for field in props(old) & props(current):
            old_type = old["properties"][field].get("type")
            new_type = current["properties"][field].get("type")
            if old_type != new_type:
                failures.append(f"{name}.{field}: changed type {old_type!r} -> {new_type!r}")
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
