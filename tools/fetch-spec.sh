#!/usr/bin/env bash
# Fetch console-external.openapi.json from the hyperfluid monorepo at a pinned
# ref and vendor it into ./apis. This is the cross-repo equivalent of the
# monorepo's `just openapi-gen`: the provider's generated client is only as
# correct as this spec, so the ref is pinned and the result is committed.
#
# Override the ref with HYPERFLUID_SPEC_REF, or copy from a local monorepo
# checkout with HYPERFLUID_MONOREPO=/path/to/hyperfluid (skips the network).
set -euo pipefail

REF="${HYPERFLUID_SPEC_REF:-c18afc13e06564eaf04d810d11bf48f330d70a2d}"
SPEC_PATH="apis/generated/console-external.openapi.json"
OUT="apis/console-external.openapi.json"

mkdir -p apis

if [[ -n "${HYPERFLUID_MONOREPO:-}" ]]; then
  cp "$HYPERFLUID_MONOREPO/$SPEC_PATH" "$OUT"
  echo "fetch-spec: copied $SPEC_PATH from local checkout $HYPERFLUID_MONOREPO"
else
  gh api "repos/nudibranches-tech/hyperfluid/contents/$SPEC_PATH?ref=$REF" \
    --jq '.content' | base64 -d >"$OUT"
  echo "fetch-spec: fetched $SPEC_PATH from nudibranches-tech/hyperfluid@$REF"
fi
