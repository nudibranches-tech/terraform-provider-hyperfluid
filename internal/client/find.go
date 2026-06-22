// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

// find returns a pointer to the first item matching pred, or ErrNotFound. This
// is the shared list-then-filter primitive behind every by-name lookup — data
// sources resolve a console/CLI-created object by listing the collection and
// matching on name, so the filter + not-found mapping lives here once.
func find[T any](items []T, pred func(*T) bool) (*T, error) {
	for i := range items {
		if pred(&items[i]) {
			return &items[i], nil
		}
	}
	return nil, ErrNotFound
}

// findByName is find specialized to a single name field — the common case.
func findByName[T any](items []T, want string, nameOf func(*T) string) (*T, error) {
	return find(items, func(t *T) bool { return nameOf(t) == want })
}
