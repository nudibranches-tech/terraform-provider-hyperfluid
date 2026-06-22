// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"testing"
)

func TestFindByName(t *testing.T) {
	type item struct {
		name string
		id   int
	}
	items := []item{{"alpha", 1}, {"beta", 2}, {"gamma", 3}}
	nameOf := func(i *item) string { return i.name }

	t.Run("found returns a pointer into the slice", func(t *testing.T) {
		got, err := findByName(items, "beta", nameOf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != &items[1] {
			t.Fatalf("want &items[1], got %+v", got)
		}
	})

	t.Run("missing returns ErrNotFound", func(t *testing.T) {
		_, err := findByName(items, "delta", nameOf)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("empty slice returns ErrNotFound", func(t *testing.T) {
		_, err := findByName([]item{}, "alpha", nameOf)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})
}

func TestFindPredicate(t *testing.T) {
	type env struct {
		slug, name string
	}
	envs := []env{{"default", "Default"}, {"prod", "Production"}}
	// Mirrors FindEnv: match on slug OR display name.
	bySlugOrName := func(want string) func(*env) bool {
		return func(e *env) bool { return e.slug == want || e.name == want }
	}

	if _, err := find(envs, bySlugOrName("Production")); err != nil {
		t.Fatalf("match by name: unexpected error: %v", err)
	}
	if _, err := find(envs, bySlugOrName("default")); err != nil {
		t.Fatalf("match by slug: unexpected error: %v", err)
	}
	if _, err := find(envs, bySlugOrName("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
