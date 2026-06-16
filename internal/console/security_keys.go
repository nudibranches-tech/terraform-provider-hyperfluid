// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package console

// The generated client (console.gen.go) emits the security-scheme scope
// constants (Api_keyScopes, Shared_secretScopes) but not their unexported key
// types, which oapi-codegen only emits in its server templates — and we
// generate client-only. We supply them here so generation stays pure (no
// post-processing) and the spec-drift gate keeps working. Unused by the
// provider; present solely to satisfy the generated constants.
type (
	apiKeyContextKey       string
	sharedSecretContextKey string
)
