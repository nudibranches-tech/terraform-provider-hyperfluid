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

// fakeComputeDock serves one dock, created by the first POST. The console
// fills omitted settings the way the API does, and the dock reports
// Provisioning once before it runs.
type fakeComputeDock struct {
	*fakeAPI
	dock     console.ComputeDockResponse
	spec     console.ComputeDockCrdSpecResponse
	polls    int
	gone     bool
	orphaned []string
}

func newFakeComputeDock(t *testing.T) (*fakeComputeDock, *computeDockResource) {
	f, pd := newFakeAPI(t)
	fd := &fakeComputeDock{fakeAPI: f}
	or := func(p *string, d string) string {
		if p == nil {
			return d
		}
		return *p
	}
	partitions := func(in []console.PartitionInput) []console.ComputeDockCrdPartition {
		out := make([]console.ComputeDockCrdPartition, 0, len(in))
		for _, p := range in {
			cp := console.ComputeDockCrdPartition{Name: p.Name, Cpu: or(p.Cpu, "2"), Memory: or(p.Memory, "4Gi"), Gpu: p.Gpu, WalltimeMax: p.WalltimeMax, NodeMax: 4}
			if p.NodeMax != nil {
				cp.NodeMax = *p.NodeMax
			}
			if p.NodeMin != nil {
				cp.NodeMin = *p.NodeMin
			}
			if p.Default != nil {
				cp.Default = *p.Default
			}
			out = append(out, cp)
		}
		return out
	}
	f.on("POST", `/harbors/[^/]+/compute-docks/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateComputeDockCrdRequestBody](t, raw)
		running := "Provisioning"
		fd.dock = console.ComputeDockResponse{
			Id: uuid.New(), HarborId: f.env, OrganizationId: f.org, Name: req.Name, Slug: req.Name, Engine: "slurm",
			Enabled: true, Phase: &running, Tags: derefStrings(req.Tags), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		fd.spec = console.ComputeDockCrdSpecResponse{
			Engine: "slurm", Enabled: true, RestApiEnabled: true, RestApiReplicas: 1, LoginEnabled: true, LoginReplicas: 1,
			HomeSize: or(req.HomeSize, "20Gi"), Version: req.Version, Partitions: partitions(req.Partitions),
		}
		if req.AutoSuspend != nil && *req.AutoSuspend.Enabled {
			fd.spec.AutoSuspendEnabled, fd.spec.AutoSuspendIdleMinutes = true, req.AutoSuspend.IdleMinutes
		}
		return 201, fd.dock
	})
	f.on("GET", `/compute-docks/[^/]+/crd`, func(string, []byte) (int, any) {
		if fd.gone {
			return 404, map[string]string{"message": "not found"}
		}
		return 200, fd.spec
	})
	f.on("GET", `/compute-docks/[^/]+`, func(string, []byte) (int, any) {
		if fd.gone {
			return 404, map[string]string{"message": "not found"}
		}
		fd.polls++
		if fd.polls > 1 {
			phase := "Running"
			if !fd.dock.Enabled {
				phase = "Suspended"
			}
			fd.dock.Phase = &phase
		}
		return 200, fd.dock
	})
	f.on("PATCH", `/compute-docks/[^/]+/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.PatchComputeDockCrdRequestBody](t, raw)
		if req.Partitions != nil {
			fd.spec.Partitions = partitions(*req.Partitions)
		}
		if req.Enabled != nil {
			fd.spec.Enabled, fd.dock.Enabled = *req.Enabled, *req.Enabled
		}
		if req.AutoSuspend != nil {
			fd.spec.AutoSuspendEnabled = *req.AutoSuspend.Enabled
			fd.spec.AutoSuspendIdleMinutes = req.AutoSuspend.IdleMinutes
		}
		return 200, console.PatchComputeDockCrdResponse{OrphanedPartitions: append([]string{}, fd.orphaned...)}
	})
	f.on("DELETE", `/compute-docks/[^/]+/crd`, func(string, []byte) (int, any) {
		fd.gone = true
		return 204, nil
	})
	return fd, &computeDockResource{p: pd, interval: time.Millisecond}
}

func partitionValue(name, cpu, memory string, nodeMax int64) attr.Value {
	return types.ObjectValueMust(computeDockPartitionType.AttrTypes, map[string]attr.Value{
		"name": types.StringValue(name), "cpu": types.StringValue(cpu), "memory": types.StringValue(memory),
		"gpu": types.Int64Null(), "node_min": types.Int64Unknown(), "node_max": types.Int64Value(nodeMax),
		"walltime_max": types.StringNull(), "default": types.BoolUnknown(),
	})
}

func computeDockPlan(f *fakeAPI) computeDockModel {
	return computeDockModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("hpc"),
		Tags:       types.ListNull(types.StringType),
		Partitions: types.ListValueMust(computeDockPartitionType, []attr.Value{partitionValue("cpu", "8", "32Gi", 2)}),
		Engine:     types.StringUnknown(), Enabled: types.BoolUnknown(), Version: types.StringUnknown(),
		HomeSize: types.StringUnknown(), HomeStorageClass: types.StringUnknown(),
		RestAPIEnabled: types.BoolUnknown(), RestAPIReplicas: types.Int64Unknown(),
		LoginEnabled: types.BoolUnknown(), LoginReplicas: types.Int64Unknown(), AccountingEnabled: types.BoolUnknown(),
		Slug: types.StringUnknown(), Phase: types.StringUnknown(), SSHEndpoint: types.StringUnknown(), RestEndpoint: types.StringUnknown(),
	}
}

func TestComputeDockSchema(t *testing.T) {
	assertSchemaImplementation(t, NewComputeDockResource())
}

// Omitted settings stay out of the request and read back as the platform
// resolved them; the create waits for Running.
func TestComputeDockCreateLeavesDefaultsToThePlatform(t *testing.T) {
	f, r := newFakeComputeDock(t)
	l := newLifecycle[computeDockModel](t, r)
	got, d := l.create(computeDockPlan(f.fakeAPI))
	mustSucceed(t, d)

	body := f.calls("POST", `/compute-docks/crd`)[0].body
	for _, k := range []string{"version", "home_size", "engine", "enabled", "rest_api_replicas", "login_enabled", "auto_suspend"} {
		if _, ok := body[k]; ok {
			t.Errorf("create body carries %q, which the config does not set: %v", k, body)
		}
	}
	if got.Phase.ValueString() != "Running" || got.HomeSize.ValueString() != "20Gi" || !got.Version.IsNull() {
		t.Errorf("state = %+v", got)
	}
	if f.polls < 2 {
		t.Errorf("polled %d times, want the wait to pass Provisioning", f.polls)
	}
}

// A changed partition list is sent whole; dropped partitions surface as a
// warning, and auto-suspend turns off when its value is removed.
func TestComputeDockUpdate(t *testing.T) {
	f, r := newFakeComputeDock(t)
	l := newLifecycle[computeDockModel](t, r)
	plan := computeDockPlan(f.fakeAPI)
	plan.AutoSuspendIdleMinutes = types.Int64Value(30)
	created, d := l.create(plan)
	mustSucceed(t, d)
	if created.AutoSuspendIdleMinutes.ValueInt64() != 30 {
		t.Fatalf("auto_suspend_idle_minutes = %v", created.AutoSuspendIdleMinutes)
	}

	next := created
	next.Partitions = types.ListValueMust(computeDockPartitionType, []attr.Value{partitionValue("gpu", "16", "64Gi", 1)})
	next.AutoSuspendIdleMinutes = types.Int64Null()
	f.orphaned = []string{"cpu"}
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if len(d.Warnings()) != 1 {
		t.Errorf("warnings = %v, want the orphaned partition", d.Warnings())
	}
	body := f.calls("PATCH", `/crd`)[0].body
	if p, _ := body["partitions"].([]any); len(p) != 1 {
		t.Errorf("PATCH partitions = %v", body["partitions"])
	}
	if as, _ := body["auto_suspend"].(map[string]any); as["enabled"] != false {
		t.Errorf("PATCH auto_suspend = %v, want disabled", body["auto_suspend"])
	}
	if _, ok := body["version"]; ok {
		t.Error("PATCH carries version, which did not change")
	}
	if !updated.AutoSuspendIdleMinutes.IsNull() {
		t.Errorf("auto_suspend_idle_minutes = %v", updated.AutoSuspendIdleMinutes)
	}
}

// A disabled dock settles Suspended.
func TestComputeDockDisabledSettlesSuspended(t *testing.T) {
	f, r := newFakeComputeDock(t)
	l := newLifecycle[computeDockModel](t, r)
	created, d := l.create(computeDockPlan(f.fakeAPI))
	mustSucceed(t, d)
	next := created
	next.Enabled = types.BoolValue(false)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if updated.Phase.ValueString() != "Suspended" {
		t.Errorf("phase = %v", updated.Phase)
	}
}

// oci_jobs is in no read view: it is carried from state, and import leaves it null.
func TestComputeDockImportAndDrift(t *testing.T) {
	f, r := newFakeComputeDock(t)
	l := newLifecycle[computeDockModel](t, r)
	plan := computeDockPlan(f.fakeAPI)
	plan.OciJobs = types.BoolValue(true)
	created, d := l.create(plan)
	mustSucceed(t, d)
	read, _, d := l.read(created)
	mustSucceed(t, d)
	if !read.OciJobs.ValueBool() {
		t.Error("oci_jobs was not carried through a read")
	}
	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported.Name.ValueString() != "hpc" || !imported.OciJobs.IsNull() {
		t.Errorf("imported = %+v", imported)
	}
	f.gone = true
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a dock deleted outside Terraform stayed in state")
	}
}

func TestComputeDockDelete(t *testing.T) {
	f, r := newFakeComputeDock(t)
	l := newLifecycle[computeDockModel](t, r)
	created, d := l.create(computeDockPlan(f.fakeAPI))
	mustSucceed(t, d)
	mustSucceed(t, l.destroy(created))
}

func TestComputeDockFeatureDisabled(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("POST", `/compute-docks/crd`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": "Your organization does not have access to the Compute Docks feature"}
	})
	l := newLifecycle[computeDockModel](t, &computeDockResource{p: pd})
	_, d := l.create(computeDockPlan(f))
	mustFail(t, d, "compute_docks_enabled")
}
