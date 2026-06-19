// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

//go:build generate

package tools

// Generate the typed Console API client from the vendored OpenAPI spec.
// oapi-codegen is pinned via a `tool` directive in tools/go.mod. This step is
// offline and deterministic (reads the committed spec, no network).
//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../apis/console-external.openapi.json

// Generate the Terraform Registry docs (provider + resource/data-source pages)
// from the schema MarkdownDescriptions and examples/. tfplugindocs is pinned via a
// `tool` directive in tools/go.mod. Unlike the client codegen this needs a
// terraform/tofu binary (CI's generate job installs one); see gen-docs.sh for why
// it extracts the schema through a dev_overrides config rather than resolving the
// provider from a registry.
//go:generate ./gen-docs.sh
