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

// fakeNextflow serves one deployment, created by the first POST. Its CRD
// reports the spec unobserved on the first read after each write, so every
// write is followed by a wait; the spec keeps what the PATCH sends.
type fakeNextflow struct {
	*fakeAPI
	nf      console.NextflowResponse
	crd     console.NextflowCrdResponse
	pending bool
	gone    bool
	failure string
}

func newFakeNextflow(t *testing.T) (*fakeNextflow, *nextflowResource) {
	f, pd := newFakeAPI(t)
	fn := &fakeNextflow{fakeAPI: f}
	f.on("POST", `/harbors/[^/]+/nextflows/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateNextflowCrdRequestBody](t, raw)
		fn.nf = console.NextflowResponse{
			Id: uuid.New(), HarborId: f.env, OrganizationId: f.org, Name: req.Name, Slug: req.Name,
			Status: "Pending", Tags: derefStrings(req.Tags), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if req.Description != nil {
			fn.nf.Description = *req.Description
		}
		fn.crd = console.NextflowCrdResponse{
			Buckets: derefStrings(req.Buckets), RuntimeImage: req.RuntimeImage,
			Egress: req.Egress, Head: req.Head, Scm: req.Scm, Storage: req.Storage, TaskQuota: req.TaskQuota,
			Status: console.NextflowCrdStatusResponse{Phase: "Ready", GrantedBuckets: []string{}, UnresolvedAllowlists: []string{}},
		}
		if req.Config != nil {
			fn.crd.Config = *req.Config
		}
		fn.pending = true
		return 201, fn.nf
	})
	f.on("GET", `/nextflows/[^/]+/crd`, func(string, []byte) (int, any) {
		if fn.gone {
			return 404, map[string]string{"message": "not found"}
		}
		out := fn.crd
		out.Status.SpecObserved = !fn.pending
		fn.pending = false
		if fn.failure != "" {
			out.Status.Phase, out.Status.Message, out.Status.SpecObserved = "Error", &fn.failure, true
		}
		return 200, out
	})
	f.on("GET", `/nextflows/[^/]+`, func(string, []byte) (int, any) {
		if fn.gone {
			return 404, map[string]string{"message": "not found"}
		}
		return 200, fn.nf
	})
	f.on("PATCH", `/nextflows/[^/]+/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.PatchNextflowCrdRequestBody](t, raw)
		raw2 := decodeInto[map[string]any](t, raw)
		if req.Config != nil {
			fn.crd.Config = *req.Config
		}
		if req.Head != nil {
			fn.crd.Head = req.Head
			if req.Head.Cpu == nil && req.Head.Memory == nil {
				fn.crd.Head = nil
			}
		}
		if v, ok := raw2["scm"]; ok {
			if v == nil {
				fn.crd.Scm = nil
			} else {
				fn.crd.Scm = req.Scm
			}
		}
		if req.Description != nil {
			fn.nf.Description = *req.Description
		}
		fn.pending = true
		return 204, nil
	})
	f.on("DELETE", `/nextflows/[^/]+/crd`, func(string, []byte) (int, any) {
		fn.gone = true
		return 204, nil
	})
	return fn, &nextflowResource{p: pd, interval: time.Millisecond}
}

func nextflowPlan(f *fakeAPI) nextflowModel {
	return nextflowModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("genomics"),
		Tags: types.ListNull(types.StringType), Buckets: types.SetNull(types.StringType),
		Egress: types.ObjectNull(nextflowEgressType.AttrTypes), Head: types.ObjectNull(nextflowHeadType.AttrTypes),
		TaskQuota: types.ObjectNull(nextflowTaskQuotaType.AttrTypes), Storage: types.ObjectUnknown(nextflowStorageType.AttrTypes),
		Scm:  types.ObjectNull(nextflowScmType.AttrTypes),
		Slug: types.StringUnknown(), Phase: types.StringUnknown(), TaskNamespace: types.StringUnknown(),
		ResolvedRuntimeImage: types.StringUnknown(), WorkVolumeCapacity: types.StringUnknown(),
	}
}

func TestNextflowSchema(t *testing.T) {
	assertSchemaImplementation(t, NewNextflowResource())
}

// An omitted image, storage, head and quota stay out of the request, so the
// platform resolves them; the create waits for the operator to observe the spec.
func TestNextflowCreateLeavesDefaultsToThePlatform(t *testing.T) {
	f, r := newFakeNextflow(t)
	l := newLifecycle[nextflowModel](t, r)
	got, d := l.create(nextflowPlan(f.fakeAPI))
	mustSucceed(t, d)

	body := f.calls("POST", `/nextflows/crd`)[0].body
	for _, k := range []string{"runtime_image", "storage", "head", "task_quota", "egress", "scm", "config", "buckets"} {
		if _, ok := body[k]; ok {
			t.Errorf("create body carries %q, which the config does not set: %v", k, body)
		}
	}
	if n := len(f.calls("GET", `/nextflows/[^/]+/crd`)); n < 3 {
		t.Errorf("crd read %d times, want the wait to poll past the unobserved spec", n)
	}
	if got.Phase.ValueString() != "Ready" || !got.Storage.IsNull() || !got.RuntimeImage.IsNull() {
		t.Errorf("state = %+v", got)
	}
}

// Removing a block clears it the way the API takes it: an empty object for
// head, an empty string for config and null for scm.
func TestNextflowUpdateClearsRemovedBlocks(t *testing.T) {
	f, r := newFakeNextflow(t)
	l := newLifecycle[nextflowModel](t, r)
	plan := nextflowPlan(f.fakeAPI)
	plan.Config = types.StringValue("process.executor = 'k8s'")
	plan.Head = types.ObjectValueMust(nextflowHeadType.AttrTypes, map[string]attr.Value{"cpu": types.StringValue("1"), "memory": types.StringNull()})
	plan.Scm = types.ObjectValueMust(nextflowScmType.AttrTypes, map[string]attr.Value{
		"platform": types.StringValue("github"), "token_secret": types.StringValue("gh-token"),
		"server": types.StringNull(), "user": types.StringNull(),
	})
	created, d := l.create(plan)
	mustSucceed(t, d)
	if !created.Head.Equal(plan.Head) || !created.Scm.Equal(plan.Scm) {
		t.Fatalf("created = %+v", created)
	}

	next := created
	next.Config = types.StringNull()
	next.Head = types.ObjectNull(nextflowHeadType.AttrTypes)
	next.Scm = types.ObjectNull(nextflowScmType.AttrTypes)
	updated, d := l.update(next, created)
	mustSucceed(t, d)

	body := f.calls("PATCH", `/crd`)[0].body
	if head, ok := body["head"].(map[string]any); !ok || len(head) != 0 {
		t.Errorf("PATCH head = %v, want {}", body["head"])
	}
	if v, ok := body["scm"]; !ok || v != nil {
		t.Errorf("PATCH scm = %v (present %v), want null", v, ok)
	}
	if body["config"] != "" {
		t.Errorf("PATCH config = %v, want empty", body["config"])
	}
	if _, ok := body["storage"]; ok {
		t.Error("PATCH carries storage, which did not change")
	}
	if !updated.Head.IsNull() || !updated.Scm.IsNull() || !updated.Config.IsNull() {
		t.Errorf("updated = %+v", updated)
	}
}

// An unchanged deployment patches nothing but the field that changed.
func TestNextflowUpdateSendsOnlyChanges(t *testing.T) {
	f, r := newFakeNextflow(t)
	l := newLifecycle[nextflowModel](t, r)
	created, d := l.create(nextflowPlan(f.fakeAPI))
	mustSucceed(t, d)
	next := created
	next.Description = types.StringValue("RNA-seq")
	_, d = l.update(next, created)
	mustSucceed(t, d)
	body := f.calls("PATCH", `/crd`)[0].body
	if len(body) != 1 || body["description"] != "RNA-seq" {
		t.Errorf("PATCH = %v, want only description", body)
	}
}

func TestNextflowImport(t *testing.T) {
	f, r := newFakeNextflow(t)
	l := newLifecycle[nextflowModel](t, r)
	created, d := l.create(nextflowPlan(f.fakeAPI))
	mustSucceed(t, d)
	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported.Name.ValueString() != "genomics" || imported.Env.ValueString() != f.env.String() {
		t.Errorf("imported = %+v", imported)
	}
}

func TestNextflowReadRemovesWhenGone(t *testing.T) {
	f, r := newFakeNextflow(t)
	l := newLifecycle[nextflowModel](t, r)
	created, d := l.create(nextflowPlan(f.fakeAPI))
	mustSucceed(t, d)
	f.gone = true
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a deployment deleted outside Terraform stayed in state")
	}
}

func TestNextflowDeleteWaitsUntilGone(t *testing.T) {
	f, r := newFakeNextflow(t)
	l := newLifecycle[nextflowModel](t, r)
	created, d := l.create(nextflowPlan(f.fakeAPI))
	mustSucceed(t, d)
	mustSucceed(t, l.destroy(created))
	if len(f.calls("DELETE", `/crd`)) != 1 {
		t.Error("no DELETE sent")
	}
}

// The Error phase ends the wait at once, with the operator's reason.
func TestNextflowErrorPhaseFailsFast(t *testing.T) {
	f, r := newFakeNextflow(t)
	l := newLifecycle[nextflowModel](t, r)
	msg := "storage class nfs is not ReadWriteMany"
	f.failure = msg
	_, d := l.create(nextflowPlan(f.fakeAPI))
	mustFail(t, d, msg)
}

// A 403 for a disabled feature names the entitlement to ask for.
func TestNextflowFeatureDisabled(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("POST", `/nextflows/crd`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": "Your organization does not have access to the Nextflow feature"}
	})
	l := newLifecycle[nextflowModel](t, &nextflowResource{p: pd})
	_, d := l.create(nextflowPlan(f))
	mustFail(t, d, "nextflow_enabled")
}
