// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeCaches serves one ready cache, created by the first POST, whose spec
// keeps resources the way the API does: an omitted field leaves them as they
// are, anything else replaces them.
func fakeCaches(t *testing.T) (*fakeAPI, *keyValueCacheResource) {
	f, pd := newFakeAPI(t)
	host, port := "sessions.ns.svc", int32(6379)
	var c console.HfKeyValueCacheResponse
	var spec console.HfKeyValueCacheCrdSpecResponse
	f.on("POST", `/hf-key-value-caches/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateHfKeyValueCacheCrdRequestBody](t, raw)
		c = console.HfKeyValueCacheResponse{
			Id: uuid.New(), HarborId: f.env, OrganizationId: f.org, Name: req.Name, Slug: req.Name, Image: "valkey:8",
			Host: &host, Port: &port, Tags: []string{}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		spec = console.HfKeyValueCacheCrdSpecResponse{Image: "valkey:8", Resources: req.Resources}
		return 201, c
	})
	f.on("GET", `/hf-key-value-caches/[^/]+/crd`, func(string, []byte) (int, any) { return 200, spec })
	f.on("GET", `/hf-key-value-caches/[^/]+`, func(string, []byte) (int, any) { return 200, c })
	f.on("PATCH", `/hf-key-value-caches/[^/]+/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.PatchHfKeyValueCacheCrdRequestBody](t, raw)
		if req.Resources != nil {
			spec.Resources = req.Resources
		}
		return 200, nil
	})
	return f, &keyValueCacheResource{p: pd}
}

func quantities(cpu, memory string) types.Object {
	v := func(s string) types.String {
		if s == "" {
			return types.StringNull()
		}
		return types.StringValue(s)
	}
	return types.ObjectValueMust(resourceQuantitiesType.AttrTypes, map[string]attr.Value{"cpu": v(cpu), "memory": v(memory)})
}

func cacheResources(requests, limits types.Object) types.Object {
	return types.ObjectValueMust(cacheResourcesType.AttrTypes, map[string]attr.Value{"requests": requests, "limits": limits})
}

func cachePlan(f *fakeAPI) keyValueCacheModel {
	return keyValueCacheModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("sessions"),
		Image: types.StringUnknown(), Maxmemory: types.StringUnknown(), MaxmemoryPolicy: types.StringUnknown(),
		Description: types.StringNull(), Tags: types.ListUnknown(types.StringType),
		Resources: types.ObjectNull(cacheResourcesType.AttrTypes),
		Host:      types.StringUnknown(), Port: types.Int64Unknown(), CredentialsSecretName: types.StringUnknown(),
		Phase: types.StringUnknown(), Slug: types.StringUnknown(),
	}
}

// Resources reach the create request and read back from the spec as written.
func TestKeyValueCacheCreateWithResources(t *testing.T) {
	f, r := fakeCaches(t)
	l := newLifecycle[keyValueCacheModel](t, r)
	plan := cachePlan(f)
	plan.Resources = cacheResources(quantities("100m", "256Mi"), quantities("", "512Mi"))

	got, d := l.create(plan)
	mustSucceed(t, d)
	res, _ := f.calls("POST", `/crd`)[0].body["resources"].(map[string]any)
	limits, _ := res["limits"].(map[string]any)
	if _, ok := limits["cpu"]; ok || limits["memory"] != "512Mi" {
		t.Errorf("create resources = %v, want no CPU limit", res)
	}
	if !got.Resources.Equal(plan.Resources) {
		t.Errorf("resources = %v, want %v", got.Resources, plan.Resources)
	}
}

// A cache without resources sends none and reads back null.
func TestKeyValueCacheCreateWithoutResources(t *testing.T) {
	f, r := fakeCaches(t)
	l := newLifecycle[keyValueCacheModel](t, r)
	got, d := l.create(cachePlan(f))
	mustSucceed(t, d)
	if _, ok := f.calls("POST", `/crd`)[0].body["resources"]; ok {
		t.Error("create body carries resources, which the config does not set")
	}
	if !got.Resources.IsNull() {
		t.Errorf("resources = %v, want null", got.Resources)
	}
}

// A changed block replaces the resources, an unchanged one is not sent, and a
// removed one is sent empty, which clears them.
func TestKeyValueCacheUpdateResources(t *testing.T) {
	f, r := fakeCaches(t)
	l := newLifecycle[keyValueCacheModel](t, r)
	plan := cachePlan(f)
	plan.Resources = cacheResources(quantities("100m", "256Mi"), types.ObjectNull(resourceQuantitiesType.AttrTypes))
	created, d := l.create(plan)
	mustSucceed(t, d)

	next := created
	next.Resources = cacheResources(quantities("250m", "256Mi"), types.ObjectNull(resourceQuantitiesType.AttrTypes))
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if res, _ := f.calls("PATCH", `/crd`)[0].body["resources"].(map[string]any); res["requests"] == nil {
		t.Errorf("PATCH resources = %v", res)
	}
	if !updated.Resources.Equal(next.Resources) {
		t.Errorf("resources = %v", updated.Resources)
	}

	next = updated
	next.Description = types.StringValue("sessions")
	_, d = l.update(next, updated)
	mustSucceed(t, d)
	if _, ok := f.calls("PATCH", `/crd`)[1].body["resources"]; ok {
		t.Error("unchanged resources were re-sent")
	}

	next = updated
	next.Resources = types.ObjectNull(cacheResourcesType.AttrTypes)
	cleared, d := l.update(next, updated)
	mustSucceed(t, d)
	if res, ok := f.calls("PATCH", `/crd`)[2].body["resources"].(map[string]any); !ok || len(res) != 0 {
		t.Errorf("PATCH resources = %v, want {}", f.calls("PATCH", `/crd`)[2].body["resources"])
	}
	if !cleared.Resources.IsNull() {
		t.Errorf("resources = %v, want null", cleared.Resources)
	}
}
