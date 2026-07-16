#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
source "$repo_root/scripts/check-dockerfile-drift.sh"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/dev" <<'EOF'
FROM scratch
ENV TROVE_ONE=one \
    TROVE_TWO=two
EOF

cat >"$tmp/same" <<'EOF'
FROM scratch
ENV TROVE_ONE=one \
    TROVE_TWO=two
EOF

cat >"$tmp/drift" <<'EOF'
FROM scratch
ENV TROVE_ONE=one \
    TROVE_TWO=changed
EOF

if [[ $(final_stage_runtime "$tmp/dev") != $(final_stage_runtime "$tmp/same") ]]; then
  echo "equal continued ENV directives were reported as drift" >&2
  exit 1
fi
if [[ $(final_stage_runtime "$tmp/dev") == $(final_stage_runtime "$tmp/drift") ]]; then
  echo "changed value in continued ENV directive was not detected" >&2
  exit 1
fi

echo "ok: continued Dockerfile directives are compared atomically"
