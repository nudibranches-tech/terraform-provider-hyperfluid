#!/usr/bin/env bash
# Generate Terraform Registry documentation with tfplugindocs.
#
# tfplugindocs needs the provider's schema. Rather than let it build + resolve the
# provider from a registry (which defaults to the `hashicorp/` namespace and fails
# for our `nudibranches-tech/hyperfluid` address — see issue #3), we extract the
# schema ourselves through a `dev_overrides` CLI config (resolves the freshly built
# binary locally, no `init`, no network) and feed it via `--providers-schema`.
#
# Requires a Terraform CLI in PATH: either `terraform` or `tofu` works, and whichever
# is installed is used (terraform first). Set TF_BIN to pin a specific binary.
# Terraform is preferred because only it reports write-only attributes; a CLI that
# does not renders incomplete docs and the script refuses to write them (see the
# write-only check below, and GEN_DOCS_ALLOW_MISSING_WRITE_ONLY to override).
# Invoked by `go generate` (see tools.go) and runnable directly.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Either CLI can dump a provider schema, so use whichever one this machine has
# (terraform first, tofu second) instead of insisting on a single name. TF_BIN
# still wins when it is set, so CI can pin an exact binary.
if [[ -z "${TF_BIN:-}" ]]; then
  for candidate in terraform tofu; do
    if command -v "$candidate" >/dev/null 2>&1; then
      TF_BIN="$candidate"
      break
    fi
  done
fi
if [[ -z "${TF_BIN:-}" ]]; then
  echo "gen-docs: no terraform or tofu binary in PATH; install one or set TF_BIN" >&2
  exit 1
fi
if ! command -v "$TF_BIN" >/dev/null 2>&1; then
  echo "gen-docs: TF_BIN=$TF_BIN is not executable" >&2
  exit 1
fi
echo "gen-docs: extracting the provider schema with $TF_BIN"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Build the provider into an isolated bin dir.
GOBIN="$TMP/bin" go -C "$ROOT" install .

# dev_overrides maps our source address to the freshly built binary, so
# `providers schema` resolves it locally with no init / no network.
cat > "$TMP/dev.tfrc" <<EOF
provider_installation {
  dev_overrides {
    "nudibranches-tech/hyperfluid" = "$TMP/bin"
  }
  direct {}
}
EOF

mkdir -p "$TMP/cfg"
cat > "$TMP/cfg/main.tf" <<EOF
terraform {
  required_providers {
    hyperfluid = {
      source = "nudibranches-tech/hyperfluid"
    }
  }
}
EOF

TF_CLI_CONFIG_FILE="$TMP/dev.tfrc" "$TF_BIN" -chdir="$TMP/cfg" providers schema -json > "$TMP/schema.json"

# Not every CLI reports every schema flag, and what it drops silently disappears
# from the rendered page. OpenTofu 1.10 has no write-only attributes, so its
# `providers schema -json` omits `write_only` and tfplugindocs cannot annotate
# those arguments — CI renders with Terraform, so the result would come back as
# unexplained drift. Detect the loss instead of assuming it: the provider source
# is the source of truth for what should have been reported.
#
# Having detected it, stop: a warning in the middle of tfplugindocs output is
# missed, and the docs it would then write are wrong in a way only CI notices.
# GEN_DOCS_ALLOW_MISSING_WRITE_ONLY=1 renders anyway for someone who wants the
# degraded output on purpose.
if grep -rqE '\bWriteOnly:[[:space:]]*true' "$ROOT/internal/provider" \
  && ! grep -q '"write_only"' "$TMP/schema.json"; then
  if [[ -n "${GEN_DOCS_ALLOW_MISSING_WRITE_ONLY:-}" ]]; then
    echo "gen-docs: WARNING: $TF_BIN does not report write-only attributes; rendering" >&2
    echo "gen-docs:          anyway because GEN_DOCS_ALLOW_MISSING_WRITE_ONLY is set." >&2
    echo "gen-docs:          The write-only annotations are missing from the result —" >&2
    echo "gen-docs:          do not commit it." >&2
  else
    cat >&2 <<MSG
gen-docs: ERROR: $TF_BIN does not report write-only attributes, so the rendered
gen-docs:        docs would silently drop the write-only annotation from every
gen-docs:        argument that has one (today: hyperfluid_secret's \`value\`). CI
gen-docs:        re-renders with Terraform >= 1.11 and reports the difference as
gen-docs:        unexplained drift, so nothing was written. Either:
gen-docs:          - install Terraform >= 1.11 and re-run — it is picked ahead of
gen-docs:            tofu automatically, or
gen-docs:          - point TF_BIN at a Terraform binary you already have:
gen-docs:            TF_BIN=/path/to/terraform go generate ./...
gen-docs:        To render with the annotations missing anyway (do not commit the
gen-docs:        result): GEN_DOCS_ALLOW_MISSING_WRITE_ONLY=1 go generate ./...
MSG
    exit 1
  fi
fi

# tfplugindocs only looks up the provider under the bare short name or
# `registry.terraform.io/hashicorp/<name>`, but both CLIs key the schema by their own
# host + our namespace (e.g. `registry.opentofu.org/nudibranches-tech/hyperfluid`). Remap
# the single provider entry to the address tfplugindocs expects — this only affects
# the lookup key, not the rendered docs (those use --provider-name).
python3 - "$TMP/schema.json" <<'PY'
import json, sys
path = sys.argv[1]
doc = json.load(open(path))
schemas = doc.get("provider_schemas") or {}
key = next(k for k in schemas if k.split("/")[-1] == "hyperfluid")
doc["provider_schemas"] = {"registry.terraform.io/hashicorp/hyperfluid": schemas[key]}
json.dump(doc, open(path, "w"))
PY

# Render docs/ from the schema + examples/ + the schema MarkdownDescriptions.
# Run from tools/ so `go tool` resolves the pinned tfplugindocs; --provider-dir
# points it back at the provider root for examples/ and docs/ output.
go -C "$ROOT/tools" tool tfplugindocs generate \
  --provider-dir "$ROOT" \
  --provider-name hyperfluid \
  --rendered-provider-name "Hyperfluid" \
  --providers-schema "$TMP/schema.json"
