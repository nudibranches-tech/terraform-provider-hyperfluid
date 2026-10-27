// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakePipelines serves the pipelines created through it. An omitted schedule
// reads back as the kind's default cadence.
func fakePipelines(t *testing.T) (*fakeAPI, *pipelineResource) {
	f, pd := newFakeAPI(t)
	pipelines := map[string]*console.PipelineDetailResponse{}
	f.on("GET", `/api/v1/organizations/[^/]+`, func(string, []byte) (int, any) {
		return 200, console.Org{Id: f.org, Name: "Acme", Slug: "acme"}
	})
	f.on("POST", `/api/v2/pipelines`, func(_ string, raw []byte) (int, any) {
		body := decodeInto[map[string]any](t, raw)
		meta, _ := body["pipeline"].(map[string]any)
		schedule, ok := meta["schedule"].(string)
		if !ok {
			schedule = "*/10 * * * *"
		}
		enabled, ok := meta["enabled"].(bool)
		name, _ := meta["name"].(string)
		harbor, _ := meta["harbor_id"].(string)
		p := &console.PipelineDetailResponse{
			Id: uuid.New(), Name: name, HarborId: uuid.MustParse(harbor),
			Schedule: schedule, Suspended: ok && !enabled, Type: "markitdown", CreatedAt: time.Now(), Groups: []console.PipelineDetailGroup{},
		}
		pipelines[p.Id.String()] = p
		return 201, console.PipelineResponseData{Id: p.Id, Name: p.Name, HarborId: p.HarborId, OrgId: f.org}
	})
	f.on("GET", `/pipelines/[^/]+`, func(p string, _ []byte) (int, any) {
		if x, ok := pipelines[p[strings.LastIndex(p, "/")+1:]]; ok {
			return 200, x
		}
		return 404, map[string]string{"message": "not found"}
	})
	f.on("PATCH", `/pipelines/[^/]+`, func(p string, raw []byte) (int, any) {
		req := decodeInto[console.UpdatePipelineRequest](t, raw)
		pipelines[p[strings.LastIndex(p, "/")+1:]].Suspended = req.Suspend
		return 200, nil
	})
	f.on("DELETE", `/pipelines/[^/]+`, func(p string, _ []byte) (int, any) {
		delete(pipelines, p[strings.LastIndex(p, "/")+1:])
		return 200, nil
	})
	return f, &pipelineResource{p: pd}
}

func pipelinePlan(f *fakeAPI) pipelineModel {
	return pipelineModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("invoices"),
		Type: types.StringValue("trino"),
		Spec: types.StringValue(`{"pipeline_type":"markitdown","source":{"type":"harbor_bucket","bucket_name":"inbox"},` +
			`"metadata_storage":{"type":"iceberg","table_name":"docs"}}`),
		Schedule: types.StringUnknown(), Suspended: types.BoolUnknown(),
		PipelineType: types.StringUnknown(), CreatedAt: types.StringUnknown(),
	}
}

func TestPipelineSchema(t *testing.T) {
	assertSchemaImplementation(t, NewPipelineResource())
}

// The request carries the spec, its type, the pipeline block the provider
// owns, and the environment on bucket references; omitted settings stay absent.
func TestPipelineCreate(t *testing.T) {
	f, r := fakePipelines(t)
	l := newLifecycle[pipelineModel](t, r)
	plan := pipelinePlan(f)

	got, d := l.create(plan)
	mustSucceed(t, d)
	body := f.calls("POST", `/api/v2/pipelines`)[0].body
	meta, _ := body["pipeline"].(map[string]any)
	if body["type"] != "trino" || meta["organization_slug"] != "acme" || meta["organization_id"] != f.org.String() ||
		meta["harbor_id"] != f.env.String() || meta["name"] != "invoices" {
		t.Errorf("create body = %v", body)
	}
	for _, k := range []string{"schedule", "max_duration_minutes", "enabled", "trigger_immediate_run"} {
		if _, ok := meta[k]; ok {
			t.Errorf("pipeline block sets %s, which the configuration leaves out", k)
		}
	}
	if src, _ := body["source"].(map[string]any); src["harbor_id"] != f.env.String() {
		t.Errorf("source = %v, want the environment injected", src)
	}
	if got.Schedule.ValueString() != "*/10 * * * *" || got.Suspended.ValueBool() || got.Spec != plan.Spec ||
		got.PipelineType.ValueString() != "markitdown" {
		t.Errorf("state = %+v", got)
	}
}

// Suspending patches in place with both stated values; an untouched pipeline
// sends nothing.
func TestPipelineUpdate(t *testing.T) {
	f, r := fakePipelines(t)
	l := newLifecycle[pipelineModel](t, r)
	created, d := l.create(pipelinePlan(f))
	mustSucceed(t, d)

	_, d = l.update(created, created)
	mustSucceed(t, d)
	if n := len(f.calls("PATCH", `/pipelines/[^/]+`)); n != 0 {
		t.Errorf("%d PATCH for no change", n)
	}

	next := created
	next.Suspended = types.BoolValue(true)
	next.MaxDurationMinutes = types.Int64Value(90)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	body := f.calls("PATCH", `/pipelines/[^/]+`)[0].body
	if body["suspend"] != true || body["max_duration_minutes"] != float64(90) {
		t.Errorf("PATCH body = %v", body)
	}
	if !updated.Suspended.ValueBool() || updated.MaxDurationMinutes.ValueInt64() != 90 {
		t.Errorf("state = %+v", updated)
	}
}

func TestPipelineDriftDeleteImport(t *testing.T) {
	f, r := fakePipelines(t)
	l := newLifecycle[pipelineModel](t, r)
	created, d := l.create(pipelinePlan(f))
	mustSucceed(t, d)

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported.Name.ValueString() != "invoices" || !imported.Spec.IsNull() || !imported.Type.IsNull() {
		t.Errorf("imported = %+v", imported)
	}

	mustSucceed(t, l.destroy(created))
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a deleted pipeline stayed in state")
	}
}

func TestPipelineSpecValidation(t *testing.T) {
	s := resourceSchema(t, NewPipelineResource()).Schema
	cfg := nullConfig(t, s, nil)
	p := path.Root("spec")
	for spec, ok := range map[string]bool{
		`{"source":{}}`:                       true,
		`not json`:                            false,
		`["a"]`:                               false,
		`{"type":"trino"}`:                    false,
		`{"pipeline":{"name":"x"}}`:           false,
		`{"source":{"type":"harbor_bucket"}}`: true,
	} {
		if d := validateString(t, s, cfg, p, types.StringValue(spec)); d.HasError() == ok {
			t.Errorf("spec %s: accepted = %v, want %v", spec, !d.HasError(), ok)
		}
	}
	// After an import the spec is adopted rather than replacing the pipeline.
	if requiresReplaceString(t, s, p, types.StringNull(), types.StringValue(`{}`)) {
		t.Error("adopting a spec after import replaces the pipeline")
	}
	if !requiresReplaceString(t, s, p, types.StringValue(`{}`), types.StringValue(`{"a":1}`)) {
		t.Error("changing the spec does not replace the pipeline")
	}
}
