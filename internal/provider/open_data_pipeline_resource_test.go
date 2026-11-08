// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeOpenDataPipelines serves the connector catalogue, the ingestion endpoint
// and the generic pipeline routes the created pipeline lives on. An omitted
// schedule reads back as `one_off`, and the name is lowercased, as the console
// does.
func fakeOpenDataPipelines(t *testing.T) (*fakeAPI, *openDataPipelineResource, *[]byte) {
	f, pd := newFakeAPI(t)
	var lastCreate []byte
	pipelines := map[string]*console.PipelineDetailResponse{}
	f.on("GET", `/api/v1/organizations/[^/]+`, func(string, []byte) (int, any) {
		return 200, console.Org{Id: f.org, Name: "Acme", Slug: "acme"}
	})
	f.on("GET", `/api/v1/opendata/connectors`, func(string, []byte) (int, any) {
		return 200, console.ListConnectorsResponse{Connectors: []console.ConnectorDescriptorResponse{
			{Slug: "world_bank", ParamsSchema: map[string]any{"type": "object"}},
			{Slug: "cat_opidor", ParamsSchema: map[string]any{"type": "object"}},
		}}
	})
	f.on("POST", `/api/v1/opendata/ingestions`, func(_ string, raw []byte) (int, any) {
		lastCreate = append([]byte(nil), raw...)
		body := decodeInto[console.CreateConnectorIngestRequest](t, raw)
		schedule := "one_off"
		if body.Pipeline.Schedule != nil {
			schedule = *body.Pipeline.Schedule
		}
		p := &console.PipelineDetailResponse{
			Id: uuid.New(), Name: strings.ToLower(body.Pipeline.Name), HarborId: body.Pipeline.HarborId,
			Schedule: schedule, Suspended: body.Pipeline.Enabled != nil && !*body.Pipeline.Enabled,
			Type: "open_data", CreatedAt: time.Now(), Groups: []console.PipelineDetailGroup{},
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
	return f, &openDataPipelineResource{p: pd}, &lastCreate
}

func openDataPipelinePlan(f *fakeAPI, container string) openDataPipelineModel {
	return openDataPipelineModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("opendata-cat-opidor"),
		ConnectorSlug:   types.StringValue("cat_opidor"),
		Params:          types.StringValue(`{"include_stopped":false,"scope":"services_structures_ateliers","limit":12345678901234567}`),
		DataContainerID: types.StringValue(container),
		SchemaName:      types.StringValue("directory"), TableName: types.StringValue("cat_opidor"),
		Schedule: types.StringValue("0 6 * * *"), Suspended: types.BoolUnknown(),
		PipelineType: types.StringUnknown(), CreatedAt: types.StringUnknown(),
	}
}

func TestOpenDataPipelineSchema(t *testing.T) {
	assertSchemaImplementation(t, NewOpenDataPipelineResource())
}

// The ingestion request carries the connector, its params as an object (big
// integers intact), the destination and the pipeline block; omitted settings
// stay absent.
func TestOpenDataPipelineCreate(t *testing.T) {
	f, r, raw := fakeOpenDataPipelines(t)
	l := newLifecycle[openDataPipelineModel](t, r)
	container := uuid.NewString()
	plan := openDataPipelinePlan(f, container)

	got, d := l.create(plan)
	mustSucceed(t, d)
	calls := f.calls("POST", `/api/v1/opendata/ingestions`)
	if len(calls) != 1 {
		t.Fatalf("%d ingestion requests, want 1", len(calls))
	}
	if n := len(f.calls("POST", `/api/v2/pipelines`)); n != 0 {
		t.Errorf("%d requests to the generic create endpoint", n)
	}
	body := calls[0].body
	meta, _ := body["pipeline"].(map[string]any)
	params, _ := body["params"].(map[string]any)
	if body["connector_slug"] != "cat_opidor" || body["data_container_id"] != container ||
		body["schema_name"] != "directory" || body["table_name"] != "cat_opidor" ||
		params["scope"] != "services_structures_ateliers" || params["include_stopped"] != false {
		t.Errorf("create body = %v", body)
	}
	if meta["organization_slug"] != "acme" || meta["organization_id"] != f.org.String() ||
		meta["harbor_id"] != f.env.String() || meta["name"] != "opendata-cat-opidor" || meta["schedule"] != "0 6 * * *" {
		t.Errorf("pipeline block = %v", meta)
	}
	for _, k := range []string{"max_duration_minutes", "enabled", "trigger_immediate_run"} {
		if _, ok := meta[k]; ok {
			t.Errorf("pipeline block sets %s, which the configuration leaves out", k)
		}
	}
	// The recorded body decodes numbers as float64, so read the wire text.
	if !strings.Contains(string(*raw), `"limit":12345678901234567`) {
		t.Errorf("params lost the exact integer: %s", *raw)
	}
	if got.ID.IsUnknown() || got.Schedule.ValueString() != "0 6 * * *" || got.Suspended.ValueBool() ||
		got.Params != plan.Params || got.ConnectorSlug != plan.ConnectorSlug || got.PipelineType.ValueString() != "open_data" {
		t.Errorf("state = %+v", got)
	}
}

// A schedule left out reads back as the platform's `one_off`.
func TestOpenDataPipelineDefaultSchedule(t *testing.T) {
	f, r, _ := fakeOpenDataPipelines(t)
	l := newLifecycle[openDataPipelineModel](t, r)
	plan := openDataPipelinePlan(f, uuid.NewString())
	plan.Schedule = types.StringUnknown()
	got, d := l.create(plan)
	mustSucceed(t, d)
	meta, _ := f.calls("POST", `/api/v1/opendata/ingestions`)[0].body["pipeline"].(map[string]any)
	if _, ok := meta["schedule"]; ok {
		t.Errorf("schedule sent although left out: %v", meta)
	}
	if got.Schedule.ValueString() != "one_off" {
		t.Errorf("schedule = %s", got.Schedule)
	}
}

func openDataDriverConfig(f *fakeAPI) map[string]any {
	return map[string]any{
		"env": f.env.String(), "name": "opendata-cat-opidor", "connector_slug": "cat_opidor",
		"params":            `{"include_stopped":false,"scope":"services_structures_ateliers"}`,
		"data_container_id": uuid.NewString(), "schema_name": "directory", "table_name": "cat_opidor",
	}
}

// A slug the catalogue does not list fails the plan, naming the ones it does;
// a plan that creates nothing does not read the catalogue.
func TestOpenDataPipelineUnknownConnector(t *testing.T) {
	f, r, _ := fakeOpenDataPipelines(t)
	d := newDriver(t, r.p, "hyperfluid_open_data_pipeline")
	cfg := openDataDriverConfig(f)
	cfg["connector_slug"] = "nope"
	wantErr(t, d.plan(tfState{}, d.config(cfg)).err, "cat_opidor, world_bank")
	if n := len(f.calls("POST", `/api/v1/opendata/ingestions`)); n != 0 {
		t.Errorf("%d ingestion requests for an unknown connector", n)
	}

	cfg["connector_slug"] = "cat_opidor"
	s := d.mustApply(tfState{}, d.config(cfg))
	before := len(f.calls("GET", `/api/v1/opendata/connectors`))
	cfg["suspended"] = true
	if p := d.plan(s, d.config(cfg)); p.err != nil {
		t.Fatalf("plan: %v", p.err)
	}
	if n := len(f.calls("GET", `/api/v1/opendata/connectors`)); n != before {
		t.Errorf("an in-place plan read the catalogue %d times", n-before)
	}
	cfg["connector_slug"] = "nope"
	wantErr(t, d.plan(s, d.config(cfg)).err, "Unknown connector")
}

// A catalogue that cannot be read fails the plan rather than skipping the check.
func TestOpenDataPipelineCatalogueUnreadable(t *testing.T) {
	f, r, _ := fakeOpenDataPipelines(t)
	f.routes = append([]fakeRoute{{method: "GET", path: regexp.MustCompile(`/api/v1/opendata/connectors$`),
		handle: func(string, []byte) (int, any) { return 500, map[string]string{"message": "boom"} }}}, f.routes...)
	d := newDriver(t, r.p, "hyperfluid_open_data_pipeline")
	wantErr(t, d.plan(tfState{}, d.config(openDataDriverConfig(f))).err, "Failed to read the connector catalogue")
}

// Suspending patches the generic pipeline in place; no change sends nothing.
func TestOpenDataPipelineUpdate(t *testing.T) {
	f, r, _ := fakeOpenDataPipelines(t)
	l := newLifecycle[openDataPipelineModel](t, r)
	created, d := l.create(openDataPipelinePlan(f, uuid.NewString()))
	mustSucceed(t, d)

	_, d = l.update(created, created)
	mustSucceed(t, d)
	if n := len(f.calls("PATCH", `/pipelines/[^/]+`)); n != 0 {
		t.Errorf("%d PATCH for no change", n)
	}

	next := created
	next.Suspended = types.BoolValue(true)
	next.MaxDurationMinutes = types.Int64Value(30)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	body := f.calls("PATCH", `/pipelines/[^/]+`)[0].body
	if body["suspend"] != true || body["max_duration_minutes"] != float64(30) {
		t.Errorf("PATCH body = %v", body)
	}
	if !updated.Suspended.ValueBool() || updated.MaxDurationMinutes.ValueInt64() != 30 {
		t.Errorf("state = %+v", updated)
	}
}

// Import takes the pipeline id; what no read reports stays null, to be
// adopted from the configuration. Delete goes through the generic endpoint.
func TestOpenDataPipelineImportDelete(t *testing.T) {
	f, r, _ := fakeOpenDataPipelines(t)
	l := newLifecycle[openDataPipelineModel](t, r)
	created, d := l.create(openDataPipelinePlan(f, uuid.NewString()))
	mustSucceed(t, d)

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported.Name.ValueString() != "opendata-cat-opidor" || imported.Schedule.ValueString() != "0 6 * * *" ||
		imported.Env.ValueString() != f.env.String() {
		t.Errorf("imported = %+v", imported)
	}
	for name, v := range map[string]types.String{
		"connector_slug": imported.ConnectorSlug, "params": imported.Params, "data_container_id": imported.DataContainerID,
		"schema_name": imported.SchemaName, "table_name": imported.TableName,
	} {
		if !v.IsNull() {
			t.Errorf("import recovered %s = %s", name, v)
		}
	}

	mustSucceed(t, l.destroy(created))
	if n := len(f.calls("DELETE", `/api/v1/organizations/[^/]+/pipelines/[^/]+`)); n != 1 {
		t.Errorf("%d generic DELETE calls", n)
	}
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a deleted pipeline stayed in state")
	}
}

func TestOpenDataPipelineValidation(t *testing.T) {
	s := resourceSchema(t, NewOpenDataPipelineResource()).Schema
	cfg := nullConfig(t, s, nil)
	for params, ok := range map[string]bool{
		`{}`:                       true,
		`{"scope":"x","n":[1,2]}`:  true,
		`not json`:                 false,
		`["a"]`:                    false,
		`null`:                     false,
		`"x"`:                      false,
		`{"a":1} {"b":2}`:          false,
		`{"a":1}` + "\n":           true,
		`{"nested":{"deep":true}}`: true,
	} {
		if d := validateString(t, s, cfg, path.Root("params"), types.StringValue(params)); d.HasError() == ok {
			t.Errorf("params %q: accepted = %v, want %v", params, !d.HasError(), ok)
		}
	}
	for name, ok := range map[string]bool{
		"opendata-cat-opidor": true,
		"feed.v2":             true,
		"Opendata":            false,
		"cat_opidor":          false,
		"-feed":               false,
		"feed-":               false,
	} {
		if d := validateString(t, s, cfg, path.Root("name"), types.StringValue(name)); d.HasError() == ok {
			t.Errorf("name %q: accepted = %v, want %v", name, !d.HasError(), ok)
		}
	}
	if d := validateString(t, s, cfg, path.Root("data_container_id"), types.StringValue("rag")); !d.HasError() {
		t.Error("a data_container_id that is not a UUID was accepted")
	}
}

func TestJSONEqual(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":{"c":[1,2]}}`, "{\n  \"b\": {\"c\": [1, 2]},\n  \"a\": 1\n}", true},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{`{"a":false}`, `{}`, false},
		{`{"n":12345678901234567}`, `{"n":12345678901234568}`, false},
		{`{"a":null}`, `{"a":null}`, true},
		{`not json`, `not json`, false},
	} {
		if got := jsonEqual(c.a, c.b); got != c.want {
			t.Errorf("jsonEqual(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// params replaces the pipeline only when its value changes: a reformatting
// keeps the recorded text, and a value adopted after an import replaces nothing.
func TestOpenDataPipelineParamsPlanModifier(t *testing.T) {
	s := resourceSchema(t, NewOpenDataPipelineResource()).Schema
	p := path.Root("params")
	recorded := `{"include_stopped":false,"scope":"services_structures_ateliers"}`
	reformatted := "{\n  \"scope\": \"services_structures_ateliers\",\n  \"include_stopped\": false\n}"

	plan, replace := planParams(t, s, types.StringValue(recorded), types.StringValue(reformatted))
	if replace || plan.ValueString() != recorded {
		t.Errorf("reformatting: plan = %s, replace = %v; want the recorded text and no replacement", plan, replace)
	}
	if _, replace := planParams(t, s, types.StringValue(recorded), types.StringValue(`{"scope":"services"}`)); !replace {
		t.Error("changing params does not replace the pipeline")
	}
	if requiresReplaceString(t, s, p, types.StringNull(), types.StringValue(recorded)) {
		t.Error("adopting params after import replaces the pipeline")
	}
	// No update carries params, so a value only known at apply time replaces.
	if !requiresReplaceString(t, s, p, types.StringValue(recorded), types.StringUnknown()) {
		t.Error("params known only at apply time plan an in-place update that never reaches the platform")
	}
	if requiresReplaceString(t, s, p, types.StringNull(), types.StringUnknown()) {
		t.Error("unknown params replace a pipeline that has none recorded")
	}
	// The other destination fields keep the import-friendly replacement.
	for _, attr := range []string{"connector_slug", "data_container_id", "schema_name", "table_name"} {
		ap := path.Root(attr)
		if requiresReplaceString(t, s, ap, types.StringNull(), types.StringValue("x")) {
			t.Errorf("adopting %s after import replaces the pipeline", attr)
		}
		if !requiresReplaceString(t, s, ap, types.StringValue("x"), types.StringValue("y")) {
			t.Errorf("changing %s does not replace the pipeline", attr)
		}
	}
	for _, attr := range []string{"env", "name", "schedule"} {
		if !requiresReplaceString(t, s, path.Root(attr), types.StringValue("x"), types.StringValue("y")) {
			t.Errorf("changing %s does not replace the pipeline", attr)
		}
	}
}

// planParams runs the params plan modifiers and returns the planned value.
func planParams(t *testing.T, s schema.Schema, state, plan types.String) (types.String, bool) {
	t.Helper()
	ctx := context.Background()
	p := path.Root("params")
	attr, d := s.AttributeAtPath(ctx, p)
	if d.HasError() {
		t.Fatalf("params: %v", d)
	}
	raw := nullObject(t, s)
	req := planmodifier.StringRequest{
		Path: p, State: tfsdk.State{Schema: s, Raw: raw}, Plan: tfsdk.Plan{Schema: s, Raw: raw},
		Config: tfsdk.Config{Schema: s, Raw: raw}, StateValue: state, PlanValue: plan, ConfigValue: plan,
	}
	resp := &planmodifier.StringResponse{PlanValue: plan}
	sa, ok := attr.(schema.StringAttribute)
	if !ok {
		t.Fatal("params is not a string attribute")
	}
	for _, pm := range sa.PlanModifiers {
		pm.PlanModifyString(ctx, req, resp)
	}
	return resp.PlanValue, resp.RequiresReplace
}

// TestAccOpenDataPipelineResource creates a Cat OPIDoR pipeline, suspends it in
// place, imports it and destroys it. Skipped unless HYPERFLUID_CREDENTIALS and
// HYPERFLUID_ACC_DATA_CONTAINER_ID (an Iceberg data container of the
// `default` environment) are set.
func TestAccOpenDataPipelineResource(t *testing.T) {
	container := os.Getenv("HYPERFLUID_ACC_DATA_CONTAINER_ID")
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			if container == "" {
				t.Skip("HYPERFLUID_ACC_DATA_CONTAINER_ID not set; skipping open data pipeline acceptance test")
			}
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOpenDataPipelineConfig(container, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_open_data_pipeline.cat_opidor", "name", "tf-acc-cat-opidor"),
					resource.TestCheckResourceAttr("hyperfluid_open_data_pipeline.cat_opidor", "schedule", "0 6 * * *"),
					resource.TestCheckResourceAttr("hyperfluid_open_data_pipeline.cat_opidor", "suspended", "false"),
					resource.TestCheckResourceAttrSet("hyperfluid_open_data_pipeline.cat_opidor", "id"),
				),
			},
			{
				Config: testAccOpenDataPipelineConfig(container, true),
				Check:  resource.TestCheckResourceAttr("hyperfluid_open_data_pipeline.cat_opidor", "suspended", "true"),
			},
			{
				ResourceName:      "hyperfluid_open_data_pipeline.cat_opidor",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"connector_slug", "params", "data_container_id", "schema_name", "table_name", "max_duration_minutes",
				},
			},
		},
	})
}

func testAccOpenDataPipelineConfig(container string, suspended bool) string {
	s := "false"
	if suspended {
		s = "true"
	}
	return `
data "hyperfluid_environment" "default" {
  name = "default"
}

resource "hyperfluid_open_data_pipeline" "cat_opidor" {
  env               = data.hyperfluid_environment.default.id
  name              = "tf-acc-cat-opidor"
  connector_slug    = "cat_opidor"
  params            = jsonencode({ scope = "services_structures_ateliers", include_stopped = false })
  data_container_id = "` + container + `"
  schema_name       = "tf_acc"
  table_name        = "cat_opidor"
  schedule          = "0 6 * * *"
  suspended         = ` + s + `
}
`
}
