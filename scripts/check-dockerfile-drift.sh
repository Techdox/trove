#!/usr/bin/env bash
# Check that dev/compose Dockerfiles and their goreleaser release counterparts
# stay in sync on the bits that matter at runtime: final base image, ENV
# defaults, EXPOSE, and VOLUME. The header comments in each Dockerfile promise
# this sync; this script enforces it in CI.
#
# Pairs checked (dev -> release):
#   Dockerfile.server -> build/docker/Dockerfile.server
#   Dockerfile.agent  -> build/docker/Dockerfile.agent-docker
#   Dockerfile.agents -> build/docker/Dockerfile.agent-k8s
#   Dockerfile.agents -> build/docker/Dockerfile.agent-proxmox
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0

# Extract the final stage of a Dockerfile (everything from the last FROM on),
# then keep only runtime-relevant directives, normalising whitespace and
# stripping comments and line continuations.
final_stage_runtime() {
  local file=$1
  awk '/^FROM /{stage=""} {stage=stage $0 "\n"} END{printf "%s", stage}' "$file" \
    | sed -e 's/#.*$//' \
    | tr -d '\\' \
    | tr -s ' \t' ' ' \
    | grep -E '^ ?(FROM|ENV|EXPOSE|VOLUME) ' \
    | sed -e 's/^ //' -e 's/ $//' \
    | sort
}

check_pair() {
  local dev=$1 release=$2
  local dev_sig release_sig
  dev_sig=$(final_stage_runtime "$dev")
  release_sig=$(final_stage_runtime "$release")
  if [ "$dev_sig" != "$release_sig" ]; then
    echo "DRIFT: $dev and $release differ in final-stage runtime directives" >&2
    diff <(printf '%s\n' "$dev_sig") <(printf '%s\n' "$release_sig") >&2 || true
    fail=1
  else
    echo "ok: $dev == $release"
  fi
}

check_pair Dockerfile.server build/docker/Dockerfile.server
check_pair Dockerfile.agent  build/docker/Dockerfile.agent-docker
check_pair Dockerfile.agents build/docker/Dockerfile.agent-k8s
check_pair Dockerfile.agents build/docker/Dockerfile.agent-proxmox

exit "$fail"
