#!/usr/bin/env bash
# Generate Terraform Registry documentation with tfplugindocs.
#
# tfplugindocs needs the provider's schema. Rather than let it build + resolve the
# provider from a registry (which defaults to the `hashicorp/` namespace and fails
# for our `nudibranches-tech/hyperfluid` address — see issue #3), we extract the
# schema ourselves through a `dev_overrides` CLI config (resolves the freshly built
# binary locally, no `init`, no network) and feed it via `--providers-schema`.
#
# Requires: a `terraform`/`tofu` binary in PATH (set TF_BIN to override). Invoked by
# `go generate` (see tools.go) and runnable directly.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TF_BIN="${TF_BIN:-terraform}"

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

# tfplugindocs only looks up the provider under the bare short name or
# `registry.terraform.io/hashicorp/<name>`, but OpenTofu keys the schema by its own
# host + our namespace (`registry.opentofu.org/nudibranches-tech/hyperfluid`). Remap
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
