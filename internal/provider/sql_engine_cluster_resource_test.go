// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeClusters serves the clusters created through it, storing their
// configuration as sent, the way the platform does.
func fakeClusters(t *testing.T) (*fakeAPI, *sqlEngineClusterResource) {
	f, pd := newFakeAPI(t)
	docks := map[string]*console.DataDock{}
	exposure := map[string]*bool{}
	f.on("POST", `/api/v1/data-docks/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateDataDockRequestBody](t, raw)
		in, err := req.ConnectionKind.AsDataDockKindRequest0()
		if err != nil {
			t.Errorf("connection_kind: %v", err)
		}
		var kind console.DataDockKind
		_ = kind.FromDataDockKind0(console.DataDockKind0{Type: console.DataDockKind0TypeTrinoInternal, Content: in.Content})
		d := &console.DataDock{
			Id: uuid.New(), HarborId: req.HarborId, Name: req.Name, Slug: req.Slug, Kind: kind,
			Host: "trino.example.test", Port: 443, Status: console.DataDockStatusOnline,
		}
		docks[d.Id.String()] = d
		return 201, console.DataDockExternalResponseData{Id: d.Id, HarborId: d.HarborId, Name: d.Name, Slug: d.Slug, Status: d.Status}
	})
	f.on("GET", `/api/v1/orgs/[^/]+/data-docks`, func(string, []byte) (int, any) {
		out := []console.DataDock{}
		for _, d := range docks {
			out = append(out, *d)
		}
		return 200, out
	})
	f.on("PATCH", `/api/v1/data-docks/[^/]+`, func(p string, raw []byte) (int, any) {
		d := docks[p[len("/api/v1/data-docks/"):]]
		req := decodeInto[console.UpdateDataDockRequest](t, raw)
		if req.Description != nil {
			d.Description = *req.Description
		}
		if req.RefreshInterval != nil {
			d.RefreshInterval = *req.RefreshInterval
		}
		if req.Kind != nil {
			in, _ := req.Kind.AsUpdateDataDockRequestKind0()
			_ = d.Kind.FromDataDockKind0(console.DataDockKind0{Type: console.DataDockKind0TypeTrinoInternal, Content: in.Content})
		}
		return 200, console.DataDockExternalResponseData{Id: d.Id}
	})
	f.on("GET", `/api/v1/data-docks/[^/]+/internet-exposure`, func(p string, _ []byte) (int, any) {
		return 200, console.DataDockInternetExposureResponse{ExposeToInternet: exposure[clusterIDIn(p)]}
	})
	f.on("PUT", `/api/v1/data-docks/[^/]+/internet-exposure`, func(p string, raw []byte) (int, any) {
		v := decodeInto[console.UpdateDataDockInternetExposureRequestBody](t, raw).ExposeToInternet
		exposure[clusterIDIn(p)] = &v
		return 204, nil
	})
	f.on("DELETE", `/api/v1/data-docks/[^/]+/crd`, func(p string, _ []byte) (int, any) {
		delete(docks, clusterIDIn(p))
		return 204, nil
	})
	return f, &sqlEngineClusterResource{p: pd}
}

func clusterIDIn(p string) string {
	const prefix = "/api/v1/data-docks/"
	rest := p[len(prefix):]
	for i, c := range rest {
		if c == '/' {
			return rest[:i]
		}
	}
	return rest
}

func clusterPlan(f *fakeAPI) sqlEngineClusterModel {
	return sqlEngineClusterModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("analytics"),
		RefreshInterval: types.Int64Unknown(), WorkerReplicas: types.Int64Value(2),
		WorkerAutoScaling: types.ObjectNull(workerAutoScalingType.AttrTypes),
		Host:              types.StringUnknown(), Port: types.Int64Unknown(), Status: types.StringUnknown(),
	}
}

func TestSqlEngineClusterSchema(t *testing.T) {
	assertSchemaImplementation(t, NewSqlEngineClusterResource())
}

// A cluster sends only what its configuration sets: no tuning default reaches
// the spec, and the name is also the slug.
func TestSqlEngineClusterCreate(t *testing.T) {
	f, r := fakeClusters(t)
	l := newLifecycle[sqlEngineClusterModel](t, r)
	plan := clusterPlan(f)
	plan.WorkerMemory = types.StringValue("4GB")
	plan.Description = types.StringValue("reporting")

	got, d := l.create(plan)
	mustSucceed(t, d)
	body := f.calls("POST", `/data-docks/crd`)[0].body
	if body["slug"] != "analytics" || body["name"] != "analytics" {
		t.Errorf("create body = %v", body)
	}
	kind, _ := body["connection_kind"].(map[string]any)
	content, _ := kind["content"].(map[string]any)
	if len(content) != 2 || content["worker_memory"] != "4GB" || content["worker_replicas"] != float64(2) {
		t.Errorf("configuration = %v, want only worker_replicas and worker_memory", content)
	}
	if patch := f.calls("PATCH", `/data-docks/[^/]+`); len(patch) != 1 || patch[0].body["description"] != "reporting" {
		t.Errorf("description PATCH = %v", patch)
	}
	if len(f.calls("PUT", `/internet-exposure`)) != 0 {
		t.Error("exposure was set although the configuration leaves it out")
	}
	if got.Description.ValueString() != "reporting" || got.Status.ValueString() != "Online" || !got.ExposeToInternet.IsNull() {
		t.Errorf("state = %+v", got)
	}
	if !got.CoordinatorCPU.IsNull() {
		t.Errorf("coordinator_cpu = %v, want null", got.CoordinatorCPU)
	}
}

// A description change patches only the description; a configuration change
// sends the whole configuration.
func TestSqlEngineClusterUpdate(t *testing.T) {
	f, r := fakeClusters(t)
	l := newLifecycle[sqlEngineClusterModel](t, r)
	created, d := l.create(clusterPlan(f))
	mustSucceed(t, d)

	next := created
	next.Description = types.StringValue("renamed")
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	patches := f.calls("PATCH", `/data-docks/[^/]+`)
	if _, ok := patches[0].body["kind"]; ok {
		t.Errorf("a description change sent the configuration: %v", patches[0].body)
	}

	next = updated
	next.WorkerReplicas = types.Int64Value(4)
	next.WorkerAutoScaling = types.ObjectValueMust(workerAutoScalingType.AttrTypes, map[string]attr.Value{
		"enabled": types.BoolValue(true), "min_replicas": types.Int64Value(2), "max_replicas": types.Int64Value(6),
		"target_cpu_utilization": types.Int64Unknown(), "target_memory_utilization": types.Int64Null(),
	})
	next.ExposeToInternet = types.BoolValue(false)
	scaled, d := l.update(next, updated)
	mustSucceed(t, d)
	kind, _ := f.calls("PATCH", `/data-docks/[^/]+`)[1].body["kind"].(map[string]any)
	content, _ := kind["content"].(map[string]any)
	scaling, _ := content["worker_auto_scaling"].(map[string]any)
	if content["worker_replicas"] != float64(4) || scaling["max_replicas"] != float64(6) {
		t.Errorf("configuration = %v", content)
	}
	if _, ok := scaling["target_cpu_utilization"]; ok {
		t.Errorf("an unset target was sent: %v", scaling)
	}
	if put := f.calls("PUT", `/internet-exposure`); len(put) != 1 || put[0].body["expose_to_internet"] != false {
		t.Errorf("exposure PUT = %v", put)
	}
	if scaled.WorkerReplicas.ValueInt64() != 4 || scaled.ExposeToInternet.ValueBool() {
		t.Errorf("state = %+v", scaled)
	}
}

// A cluster deleted outside Terraform leaves the state; delete waits for it to go.
func TestSqlEngineClusterDriftAndDelete(t *testing.T) {
	f, r := fakeClusters(t)
	l := newLifecycle[sqlEngineClusterModel](t, r)
	created, d := l.create(clusterPlan(f))
	mustSucceed(t, d)

	mustSucceed(t, l.destroy(created))
	if len(f.calls("DELETE", `/crd`)) != 1 {
		t.Error("delete did not call the API")
	}
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a deleted cluster stayed in state")
	}
}

func TestSqlEngineClusterImport(t *testing.T) {
	f, r := fakeClusters(t)
	l := newLifecycle[sqlEngineClusterModel](t, r)
	plan := clusterPlan(f)
	plan.TaskConcurrency = types.Int64Value(8)
	created, d := l.create(plan)
	mustSucceed(t, d)

	got, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if got.Name.ValueString() != "analytics" || got.Env.ValueString() != f.env.String() || got.TaskConcurrency.ValueInt64() != 8 {
		t.Errorf("imported = %+v", got)
	}
}

func TestSqlEngineClusterMemoryFormat(t *testing.T) {
	s := resourceSchema(t, NewSqlEngineClusterResource()).Schema
	cfg := nullConfig(t, s, nil)
	for v, ok := range map[string]bool{"2GB": true, "1.5GB": true, "512MB": true, "2Gi": false, "2": false} {
		d := validateString(t, s, cfg, path.Root("worker_memory"), types.StringValue(v))
		if d.HasError() == ok {
			t.Errorf("worker_memory %q: accepted = %v, want %v", v, !d.HasError(), ok)
		}
	}
}

func TestSqlEngineClusterDataSource(t *testing.T) {
	f, r := fakeClusters(t)
	created, d := newLifecycle[sqlEngineClusterModel](t, r).create(clusterPlan(f))
	mustSucceed(t, d)
	ds := &sqlEngineClusterDataSource{p: r.p}

	got, d := readDataSource(t, ds, sqlEngineClusterDataModel{Env: types.StringValue(f.env.String()), Name: types.StringValue("analytics")})
	mustSucceed(t, d)
	if got.ID != created.ID || got.WorkerReplicas.ValueInt64() != 2 {
		t.Errorf("data source = %+v", got)
	}
	_, d = readDataSource(t, ds, sqlEngineClusterDataModel{Env: types.StringValue(uuid.NewString()), Name: types.StringValue("analytics")})
	mustFail(t, d, "not found")
}
