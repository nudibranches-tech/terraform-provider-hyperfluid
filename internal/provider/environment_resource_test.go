// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// envFake serves one environment through the create, list and delete routes;
// gone flips once the delete is accepted.
type envFake struct {
	*fakeAPI
	pd   *providerData
	id   uuid.UUID
	gone bool
}

func newEnvFake(t *testing.T) *envFake {
	f, pd := newFakeAPI(t)
	e := &envFake{fakeAPI: f, pd: pd, id: uuid.New()}
	harbor := func() map[string]any {
		return map[string]any{
			"id": e.id.String(), "name": "Team A", "slug": "team-a",
			"namespace": "hf-acme-team-a", "organization_id": f.org.String(),
		}
	}
	f.on(http.MethodPost, `/harbors/crd`, func(string, []byte) (int, any) { return http.StatusCreated, harbor() })
	f.on(http.MethodDelete, `/harbors/crd/team-a`, func(string, []byte) (int, any) {
		e.gone = true
		return http.StatusNoContent, nil
	})
	f.on(http.MethodGet, `/harbors`, func(string, []byte) (int, any) {
		if e.gone {
			return http.StatusOK, []any{}
		}
		return http.StatusOK, []any{harbor()}
	})
	return e
}

func (e *envFake) resource() *environmentResource {
	return &environmentResource{p: e.pd}
}

func envPlan(protected bool) environmentModel {
	return environmentModel{
		ID:                 types.StringUnknown(),
		Name:               types.StringValue("Team A"),
		Slug:               types.StringValue("team-a"),
		Namespace:          types.StringUnknown(),
		OwnerID:            types.StringUnknown(),
		DeletionProtection: types.BoolValue(protected),
	}
}

func TestEnvironmentCreateSendsOnlyNameAndSlug(t *testing.T) {
	e := newEnvFake(t)
	l := newLifecycle[environmentModel](t, e.resource())

	got, d := l.create(envPlan(true))
	mustSucceed(t, d)

	calls := e.calls(http.MethodPost, `/organizations/`+e.org.String()+`/harbors/crd`)
	if len(calls) != 1 {
		t.Fatalf("want one create call, got %d", len(calls))
	}
	if body := calls[0].body; len(body) != 2 || body["name"] != "Team A" || body["slug"] != "team-a" {
		t.Errorf("create body = %v, want only name and slug", body)
	}
	if got.ID.ValueString() != e.id.String() || got.Namespace.ValueString() != "hf-acme-team-a" {
		t.Errorf("state = %+v", got)
	}
	if !got.OwnerID.IsNull() || !got.DeletionProtection.ValueBool() {
		t.Errorf("owner_id = %v, deletion_protection = %v", got.OwnerID, got.DeletionProtection)
	}
}

func TestEnvironmentReadAndDrift(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		e := newEnvFake(t)
		l := newLifecycle[environmentModel](t, e.resource())
		prior, d := l.create(envPlan(false))
		mustSucceed(t, d)
		got, removed, d := l.read(prior)
		mustSucceed(t, d)
		if removed || got.ID != prior.ID || got.DeletionProtection.ValueBool() {
			t.Errorf("read = %+v (removed %v)", got, removed)
		}
	})
	t.Run("deleted outside Terraform", func(t *testing.T) {
		e := newEnvFake(t)
		l := newLifecycle[environmentModel](t, e.resource())
		prior, d := l.create(envPlan(true))
		mustSucceed(t, d)
		e.gone = true
		_, removed, d := l.read(prior)
		mustSucceed(t, d)
		if !removed {
			t.Error("a missing environment stayed in state")
		}
	})
	t.Run("slug taken by another environment", func(t *testing.T) {
		e := newEnvFake(t)
		l := newLifecycle[environmentModel](t, e.resource())
		prior, d := l.create(envPlan(true))
		mustSucceed(t, d)
		e.id = uuid.New()
		_, removed, d := l.read(prior)
		mustSucceed(t, d)
		if !removed {
			t.Error("an environment with another id was adopted")
		}
	})
}

func TestEnvironmentImportBySlug(t *testing.T) {
	e := newEnvFake(t)
	l := newLifecycle[environmentModel](t, e.resource())

	got, d := l.importAndRead("team-a")
	mustSucceed(t, d)
	if got.ID.ValueString() != e.id.String() || got.Name.ValueString() != "Team A" {
		t.Errorf("imported state = %+v", got)
	}
	if !got.DeletionProtection.ValueBool() {
		t.Error("an imported environment is not protected")
	}
}

func TestEnvironmentDeletionProtectionOnDestroy(t *testing.T) {
	t.Run("protected", func(t *testing.T) {
		e := newEnvFake(t)
		l := newLifecycle[environmentModel](t, e.resource())
		prior, d := l.create(envPlan(true))
		mustSucceed(t, d)
		mustFail(t, l.destroy(prior), "deletion_protection = false")
		if n := len(e.calls(http.MethodDelete, `/harbors/crd/team-a`)); n != 0 {
			t.Errorf("a protected environment was deleted (%d calls)", n)
		}
	})
	t.Run("unprotected", func(t *testing.T) {
		e := newEnvFake(t)
		l := newLifecycle[environmentModel](t, e.resource())
		prior, d := l.create(envPlan(false))
		mustSucceed(t, d)
		mustSucceed(t, l.destroy(prior))
		if n := len(e.calls(http.MethodDelete, `/harbors/crd/team-a`)); n != 1 {
			t.Errorf("want one delete call, got %d", n)
		}
	})
	t.Run("already gone", func(t *testing.T) {
		f, pd := newFakeAPI(t)
		f.on(http.MethodDelete, `/harbors/crd/team-a`, func(string, []byte) (int, any) {
			return http.StatusNotFound, "environment team-a not found"
		})
		l := newLifecycle[environmentModel](t, &environmentResource{p: pd})
		prior := envPlan(false)
		prior.ID, prior.Namespace, prior.OwnerID = types.StringValue(uuid.NewString()), types.StringValue("ns"), types.StringNull()
		mustSucceed(t, l.destroy(prior))
	})
}

func TestEnvironmentDeletionProtectionOnReplace(t *testing.T) {
	e := newEnvFake(t)
	r := e.resource()
	l := newLifecycle[environmentModel](t, r)

	modify := func(prior, plan environmentModel) *fwresource.ModifyPlanResponse {
		resp := &fwresource.ModifyPlanResponse{Plan: l.plan(plan)}
		r.ModifyPlan(t.Context(), fwresource.ModifyPlanRequest{State: l.state(prior), Plan: l.plan(plan), Config: l.config(plan)}, resp)
		return resp
	}
	protected, d := l.create(envPlan(true))
	mustSucceed(t, d)

	renamed := protected
	renamed.Slug = types.StringValue("team-b")
	mustFail(t, modify(protected, renamed).Diagnostics, "deletion_protection = false")

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	renamed.DeletionProtection = types.BoolValue(false)
	mustSucceed(t, modify(unprotected, renamed).Diagnostics)

	// Turning protection off is an in-place change, never a replacement.
	mustSucceed(t, modify(protected, unprotected).Diagnostics)
	got, d := l.update(unprotected, protected)
	mustSucceed(t, d)
	if got.DeletionProtection.ValueBool() {
		t.Error("update kept deletion_protection on")
	}
}

func TestEnvironmentCreateRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    any
		summary string
		hint    string
	}{
		{
			name: "quota",
			body: map[string]any{
				"message": "Quota exceeded for environment: using 3/3. Please upgrade your plan.",
				"code":    "QUOTA_EXCEEDED", "resource_type": "harbor", "current": 3, "limit": 3, "kind": "count",
			},
			summary: "Environment quota reached",
			hint:    "already has 3 of the 3 environments",
		},
		{
			name:    "other refusal",
			body:    map[string]any{"message": "Environments are disabled for this organization"},
			summary: "Failed to create environment",
			hint:    "Environments are disabled for this organization",
		},
		{
			name:    "permission",
			body:    map[string]any{"message": "Missing permission 'harbor:create'"},
			summary: "Missing permission to create environments",
			hint:    `missing permission "harbor:create"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, pd := newFakeAPI(t)
			f.on(http.MethodPost, `/harbors/crd`, func(string, []byte) (int, any) { return http.StatusForbidden, tc.body })
			l := newLifecycle[environmentModel](t, &environmentResource{p: pd})
			_, d := l.create(envPlan(true))
			mustFail(t, d, tc.summary)
			mustFail(t, d, tc.hint)
		})
	}
}

func TestEnvironmentSlugValidator(t *testing.T) {
	for slug, ok := range map[string]bool{"team-a": true, "a1": true, "Team-A": false, "-a": false, "a-": false, "a_b": false} {
		if environmentSlugRE.MatchString(slug) != ok {
			t.Errorf("slug %q: match = %v, want %v", slug, !ok, ok)
		}
	}
}

func TestEnvDataSourceDeprecatedAlias(t *testing.T) {
	for _, tc := range []struct {
		ds         fwdatasource.DataSource
		typeName   string
		deprecated bool
	}{
		{NewEnvironmentDataSource(), "hyperfluid_environment", false},
		{NewEnvDataSource(), "hyperfluid_env", true},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			meta := &fwdatasource.MetadataResponse{}
			tc.ds.Metadata(t.Context(), fwdatasource.MetadataRequest{ProviderTypeName: "hyperfluid"}, meta)
			if meta.TypeName != tc.typeName {
				t.Errorf("type name = %q", meta.TypeName)
			}
			if got := dataSourceSchema(t, tc.ds).Schema.DeprecationMessage != ""; got != tc.deprecated {
				t.Errorf("deprecated = %v, want %v", got, tc.deprecated)
			}

			e := newEnvFake(t)
			ds, ok := tc.ds.(*envDataSource)
			if !ok {
				t.Fatalf("%T is not an *envDataSource", tc.ds)
			}
			ds.p = e.pd
			got, d := readDataSource(t, tc.ds, envModel{Name: types.StringValue("team-a"), ID: types.StringNull()})
			mustSucceed(t, d)
			if got.ID.ValueString() != e.id.String() {
				t.Errorf("id = %v, want %s", got.ID, e.id)
			}
		})
	}
}
