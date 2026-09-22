#!/usr/bin/env bash
# Fetch console-external.openapi.json from the hyperfluid monorepo at a pinned
# ref and vendor it into ./apis. This is the cross-repo equivalent of the
# monorepo's `just openapi-gen`: the provider's generated client is only as
# correct as this spec, so the ref is pinned and the result is committed.
#
# Override the ref with HYPERFLUID_SPEC_REF, or copy from a local monorepo
# checkout with HYPERFLUID_MONOREPO=/path/to/hyperfluid (skips the network).
set -euo pipefail

REF="${HYPERFLUID_SPEC_REF:-438a8dc91816c79f2935dfcf3c3a2d97e5667ef6}"
SPEC_PATH="apis/generated/console-external.openapi.json"
OUT="apis/console-external.openapi.json"

mkdir -p apis

if [[ -n "${HYPERFLUID_MONOREPO:-}" ]]; then
  cp "$HYPERFLUID_MONOREPO/$SPEC_PATH" "$OUT"
  echo "fetch-spec: copied $SPEC_PATH from local checkout $HYPERFLUID_MONOREPO"
else
  # `--jq .content` cannot be used here: the contents API only inlines base64
  # for blobs under 1 MiB, and this spec passed that in 2026 (~2.1 MB). Over
  # the limit it answers `"content": ""` with `"encoding": "none"`, and
  # `base64 -d` of nothing succeeds — so the pipeline wrote an EMPTY spec and
  # exited 0, which `set -euo pipefail` cannot see. The raw media type streams
  # the blob itself and is good to 100 MB.
  gh api "repos/nudibranches-tech/hyperfluid/contents/$SPEC_PATH?ref=$REF" \
    -H "Accept: application/vnd.github.raw" >"$OUT"
  echo "fetch-spec: fetched $SPEC_PATH from nudibranches-tech/hyperfluid@$REF"
fi

# Whatever the source, refuse to leave a spec behind that the generator would
# read as "an API with nothing in it": that is how a fetch failure turns into a
# silently emptied client instead of a red build.
python3 - "$OUT" <<'PYCHECK'
import json, sys

path = sys.argv[1]
with open(path, encoding="utf-8") as handle:
    spec = json.load(handle)
if not spec.get("openapi"):
    sys.exit(f"fetch-spec: {path} carries no `openapi` version — refusing it")
if not spec.get("paths"):
    sys.exit(f"fetch-spec: {path} declares no paths — refusing it")
print(f"fetch-spec: {path} looks like a spec ({len(spec['paths'])} paths)")
PYCHECK
