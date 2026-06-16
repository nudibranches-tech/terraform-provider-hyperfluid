// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

//go:build generate

package tools

// Generate the typed Console API client from the vendored OpenAPI spec.
// oapi-codegen is pinned via a `tool` directive in tools/go.mod.
//
// Registry docs (tfplugindocs) are intentionally NOT wired yet — that's a
// dedicated docs task before publishing (it needs the provider resolvable under
// its registry source address). Keeping generation client-only means CI's
// generate check runs offline and deterministically.
//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../apis/console-external.openapi.json
