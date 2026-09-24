#!/usr/bin/env bash
set -euo pipefail

# Resolve the release version for the pipeline.
#
# Usage:
#   deduce-version.sh <tag>   fixed version from a pushed tag (e.g. v0.2.0)
#   deduce-version.sh         deduce the next version from conventional commits
#
# When called without arguments, the next version is computed from the commits
# since the most recent tag, following semantic versioning:
#   any breaking change (BREAKING CHANGE footer or type!)      -> major bump
#   feat: commits                                              -> minor bump
#   everything else                                            -> patch bump

emit() {
  local line="$1"
  printf '%s\n' "${line}"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf '%s\n' "${line}" >> "${GITHUB_OUTPUT}"
  fi
}

if [ $# -ge 1 ]; then
  emit "version=${1#v}"
  emit "prev_tag=$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || true)"
  exit 0
fi

prev_tag="$(git describe --tags --abbrev=0 HEAD 2>/dev/null || true)" && true

if [ -z "${prev_tag}" ]; then
  emit "version=0.1.0"
  emit "prev_tag="
  exit 0
fi

log="$(git log --no-merges --format='%s%n%b' "${prev_tag}..HEAD" 2>/dev/null || true)"
if [ -z "$(printf '%s' "${log}" | tr -d '[:space:]')" ]; then
  echo "no commits since ${prev_tag}; nothing to release" >&2
  exit 78
fi

core="${prev_tag#v}"
core="${core%%-*}"
IFS=. read -r major minor patch <<EOF
${core}
EOF
major="${major:-0}"
minor="${minor:-0}"
patch="${patch:-0}"

bump=patch
if grep -qiE '^[a-z]+(\([^)]*\))?!:' <<< "${log}" \
  || grep -qiE '(^|[[:space:]])BREAKING[- ]CHANGE:' <<< "${log}"; then
  bump=major
elif grep -qE '^feat(\([^)]*\))?:' <<< "${log}"; then
  bump=minor
fi

case "${bump}" in
  major) major=$((major + 1)); minor=0; patch=0 ;;
  minor) minor=$((minor + 1)); patch=0 ;;
  *)     patch=$((patch + 1)) ;;
esac

emit "version=${major}.${minor}.${patch}"
emit "prev_tag=${prev_tag}"