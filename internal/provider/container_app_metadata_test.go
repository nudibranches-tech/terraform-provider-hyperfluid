// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func literalApp(f *fakeConsole) containerAppModel {
	m := nullApp(f)
	m.Name = types.StringNull()
	m.ImageRepository = types.StringValue("nginx")
	m.ImageTag = types.StringValue("alpine")
	return m
}

func tagList(tags ...string) types.List {
	elems := make([]attr.Value, 0, len(tags))
	for _, t := range tags {
		elems = append(elems, types.StringValue(t))
	}
	return types.ListValueMust(types.StringType, elems)
}

// The display name, slug, description and tags all reach the create request,
// and the id comes from the create response rather than from a lookup.
func TestContainerAppCreateSendsMetadata(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.DisplayName = types.StringValue("Orders API")
	plan.Description = types.StringValue("Takes orders.")
	plan.Tags = tagList("team-a", "public")

	got, d := h.create(plan)
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	want := map[string]any{"name": "Orders API", "slug": "orders-api", "description": "Takes orders."}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("create body %s = %v, want %v", k, body[k], v)
		}
	}
	if tags, _ := body["tags"].([]any); len(tags) != 2 {
		t.Errorf("create body tags = %v, want 2 tags", body["tags"])
	}
	if n := len(h.f.calls("GET", "/container-apps")); n != 0 {
		t.Errorf("listed apps %d times; the create response carries the id", n)
	}
	if got.ID.ValueString() != h.f.appID.String() {
		t.Errorf("id = %s, want %s", got.ID, h.f.appID)
	}
	if got.Slug.ValueString() != "orders-api" || got.Name.ValueString() != "orders-api" {
		t.Errorf("slug/name = %s/%s, want orders-api for both", got.Slug, got.Name)
	}
	if got.DisplayName.ValueString() != "Orders API" || got.Description.ValueString() != "Takes orders." {
		t.Errorf("display_name/description = %s/%s", got.DisplayName, got.Description)
	}
	if !got.Tags.Equal(tagList("team-a", "public")) {
		t.Errorf("tags = %s", got.Tags)
	}
}

// A config still on the deprecated name sends it as the slug and the display
// name, which is what the provider sent before both existed.
func TestContainerAppCreateWithDeprecatedName(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Name = types.StringValue("orders-api")

	got, d := h.create(plan)
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	if body["name"] != "orders-api" || body["slug"] != "orders-api" {
		t.Errorf("create body name/slug = %v/%v, want orders-api for both", body["name"], body["slug"])
	}
	if got.Name.ValueString() != "orders-api" || got.Slug.ValueString() != "orders-api" || got.DisplayName.ValueString() != "orders-api" {
		t.Errorf("name/slug/display_name = %s/%s/%s", got.Name, got.Slug, got.DisplayName)
	}
}

// A console that answers the create without the app's id finds it by slug.
func TestContainerAppCreateOnConsoleWithoutCreateBody(t *testing.T) {
	h := newHarness(t)
	h.f.noBody = true
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")

	got, d := h.create(plan)
	noErrors(t, d)

	if n := len(h.f.calls("GET", "/container-apps")); n != 1 {
		t.Errorf("listed apps %d times, want 1", n)
	}
	if got.ID.ValueString() != h.f.appID.String() {
		t.Errorf("id = %s, want %s", got.ID, h.f.appID)
	}
}

// Changed metadata is patched in place; a removed description is sent blank.
func TestContainerAppUpdatePatchesMetadata(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.DisplayName = types.StringValue("Orders API")
	plan.Description = types.StringValue("Takes orders.")
	plan.Tags = tagList("team-a")
	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.DisplayName = types.StringValue("Orders")
	next.Description = types.StringNull()
	next.Tags = tagList()
	got, d := h.update(next, h.stateOf(created))
	noErrors(t, d)

	patches := h.f.calls("PATCH", "/container-apps/"+h.f.appID.String())
	if len(patches) != 1 {
		t.Fatalf("metadata patches = %d, want 1", len(patches))
	}
	body := patches[0].body
	if body["name"] != "Orders" || body["description"] != "" {
		t.Errorf("patch name/description = %v/%v, want Orders and a blank description", body["name"], body["description"])
	}
	if tags, ok := body["tags"].([]any); !ok || len(tags) != 0 {
		t.Errorf("patch tags = %v, want an empty list", body["tags"])
	}
	if got.DisplayName.ValueString() != "Orders" || !got.Description.IsNull() || !got.Tags.Equal(tagList()) {
		t.Errorf("state = %s/%s/%s", got.DisplayName, got.Description, got.Tags)
	}
}

// An update that leaves the metadata alone sends no metadata patch.
func TestContainerAppUpdateWithoutMetadataChange(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.Description = types.StringValue("Takes orders.")
	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.Replicas = types.Int64Value(2)
	_, d = h.update(next, h.stateOf(created))
	noErrors(t, d)

	if n := len(h.f.calls("PATCH", "/container-apps/"+h.f.appID.String())); n != 0 {
		t.Errorf("metadata patches = %d, want 0", n)
	}
}

// The API trims a description; the configured text stays in state when only
// surrounding whitespace differs, as with a heredoc.
func TestContainerAppDescriptionKeepsConfiguredWhitespace(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.Description = types.StringValue("Takes orders.\n")
	h.f.meta.Description = nil

	got, d := h.create(plan)
	noErrors(t, d)
	if got.Description.ValueString() != "Takes orders.\n" {
		t.Errorf("description = %q, want the configured text", got.Description.ValueString())
	}

	cases := []struct {
		configured, read types.String
		want             types.String
	}{
		{types.StringValue("a\n"), types.StringValue("a"), types.StringValue("a\n")},
		{types.StringValue(" "), types.StringNull(), types.StringValue(" ")},
		{types.StringValue("a"), types.StringValue("b"), types.StringValue("b")},
		{types.StringNull(), types.StringValue("b"), types.StringValue("b")},
	}
	for _, c := range cases {
		if got := keepUntrimmed(c.configured, c.read); !got.Equal(c.want) {
			t.Errorf("keepUntrimmed(%s, %s) = %s, want %s", c.configured, c.read, got, c.want)
		}
	}
}
