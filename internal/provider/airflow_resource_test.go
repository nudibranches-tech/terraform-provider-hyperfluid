// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── test helpers ──────────────────────────────────────────────────────────

// resourceSchema renders a resource's schema and asserts the framework accepts
// it. ValidateImplementation catches the schema mistakes that would otherwise
// only surface as a provider crash on first use — a Default on a
// non-Computed attribute, an attribute that is neither required, optional nor
// computed, a nested set of a type the protocol cannot carry.
func resourceSchema(t *testing.T, r fwresource.Resource) fwresource.SchemaResponse {
	t.Helper()
	ctx := t.Context()
	resp := fwresource.SchemaResponse{}
	r.Schema(ctx, fwresource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics.Errors())
	}
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema implementation: %v", d.Errors())
	}
	return resp
}

func dataSourceSchema(t *testing.T, ds fwdatasource.DataSource) fwdatasource.SchemaResponse {
	t.Helper()
	ctx := t.Context()
	resp := fwdatasource.SchemaResponse{}
	ds.Schema(ctx, fwdatasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics.Errors())
	}
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema implementation: %v", d.Errors())
	}
	return resp
}

// isNilPointer reports whether v is a nil pointer of any type — the shape
// "this field was left out of the request body" takes on the generated client.
func isNilPointer(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}

// airflowEgressObject builds an egress object value the way a configuration
// would, with an absent collection left null rather than empty.
func airflowEgressObject(t *testing.T, fqdns []string, links []airflowInClusterLinkModel, allowlists []string) types.Object {
	t.Helper()
	ctx := t.Context()

	fqdnSet := types.SetNull(types.StringType)
	if fqdns != nil {
		v, d := types.SetValueFrom(ctx, types.StringType, fqdns)
		if d.HasError() {
			t.Fatalf("building fqdns: %v", d.Errors())
		}
		fqdnSet = v
	}
	linkSet := types.SetNull(airflowInClusterLinkType())
	if links != nil {
		v, d := types.SetValueFrom(ctx, airflowInClusterLinkType(), links)
		if d.HasError() {
			t.Fatalf("building in_cluster: %v", d.Errors())
		}
		linkSet = v
	}
	allowSet := types.SetNull(types.StringType)
	if allowlists != nil {
		v, d := types.SetValueFrom(ctx, types.StringType, allowlists)
		if d.HasError() {
			t.Fatalf("building allowlists: %v", d.Errors())
		}
		allowSet = v
	}

	obj, d := types.ObjectValue(airflowEgressAttrTypes(), map[string]attr.Value{
		"fqdns":      fqdnSet,
		"in_cluster": linkSet,
		"allowlists": allowSet,
	})
	if d.HasError() {
		t.Fatalf("building the egress object: %v", d.Errors())
	}
	return obj
}

// airflowNullModel is an all-null model with every collection carrying its
// element type, which is what makes it writable into a state: a zero-value
// types.List has no element type and would fail for that reason alone, telling
// us nothing about whether the model and the schema agree.
func airflowNullModel() airflowModel {
	return airflowModel{
		Tags:                 types.ListNull(types.StringType),
		Egress:               types.ObjectNull(airflowEgressAttrTypes()),
		Config:               types.MapNull(types.StringType),
		UnresolvedAllowlists: types.ListNull(types.StringType),
	}
}

// airflowFullModel is an environment with every optional field set — the state
// a patch is diffed against.
func airflowFullModel(t *testing.T) airflowModel {
	t.Helper()
	ctx := t.Context()
	config, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"core.parallelism": "64"})
	if d.HasError() {
		t.Fatalf("building the config map: %v", d.Errors())
	}
	tags, d := types.ListValueFrom(ctx, types.StringType, []string{"prod"})
	if d.HasError() {
		t.Fatalf("building the tag list: %v", d.Errors())
	}
	return airflowModel{
		ID:               types.StringValue("11111111-1111-1111-1111-111111111111"),
		Env:              types.StringValue("22222222-2222-2222-2222-222222222222"),
		Name:             types.StringValue("etl"),
		NodeTier:         types.StringValue("small"),
		TriggererEnabled: types.BoolValue(true),
		SleepMode:        types.BoolValue(false),
		RuntimeImage:     types.StringValue("registry.example.com/airflow:3.1-etl"),
		TaskQuotaMaxPods: types.Int64Value(40),
		Description:      types.StringValue("nightly load"),
		Tags:             tags,
		Egress: airflowEgressObject(t, []string{"api.example.com"},
			[]airflowInClusterLinkModel{{Kind: types.StringValue("ManagedPostgreSQL"), Name: types.StringValue("warehouse")}},
			nil),
		Config: config,
	}
}

// ── schema / model agreement ──────────────────────────────────────────────

func TestAirflowResourceSchema(t *testing.T) {
	ctx := t.Context()
	s := resourceSchema(t, NewAirflowResource()).Schema

	// Every model field has to land in a schema attribute and vice versa.
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if d := state.Set(ctx, airflowNullModel()); d.HasError() {
		t.Fatalf("airflowModel does not fit the resource schema: %v", d.Errors())
	}

	// runtime_image must stay Optional-only. Computed would make a removed
	// attribute keep its prior value, so dropping the line from a config would
	// silently hold the environment on the pinned image for ever instead of
	// handing it back to the platform default.
	img, ok := s.Attributes["runtime_image"]
	if !ok {
		t.Fatal("runtime_image attribute missing")
	}
	if !img.IsOptional() || img.IsComputed() {
		t.Errorf("runtime_image must be Optional and NOT Computed, got optional=%v computed=%v",
			img.IsOptional(), img.IsComputed())
	}

	// Same reasoning for the two replace-not-merge collections and the quota:
	// a Computed attribute cannot express "the user removed this".
	for _, name := range []string{"egress", "config", "task_quota_max_pods"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("%s attribute missing", name)
		}
		if attr.IsComputed() {
			t.Errorf("%s must not be Computed: a removed attribute has to plan a clear", name)
		}
	}
}

// TestAirflowTypesAreRegistered asserts the four new types reach the provider's
// wire schema. A resource that exists but is not returned by Resources() is
// invisible to Terraform, and nothing else in the test suite would notice —
// this also runs the real server-side schema validation over every type the
// provider declares, not just Airflow's.
func TestAirflowTypesAreRegistered(t *testing.T) {
	ctx := t.Context()
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("starting the provider server: %v", err)
	}
	resp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	for _, diagnostic := range resp.Diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("provider schema: %s: %s", diagnostic.Summary, diagnostic.Detail)
		}
	}
	for _, name := range []string{"hyperfluid_airflow", "hyperfluid_airflow_connection"} {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("resource %q is not registered in Resources()", name)
		}
		if _, ok := resp.DataSourceSchemas[name]; !ok {
			t.Errorf("data source %q is not registered in DataSources()", name)
		}
	}
}

// ── config validation ─────────────────────────────────────────────────────

// TestAirflowValidateConfigRejectsEmptyEgress: an egress block granting nothing
// is not sent (it would freeze an empty key into the spec), so the block would
// come back absent and the apply would die as an inconsistent result. Saying so
// at plan time is the only way the user learns which line caused it.
func TestAirflowValidateConfigRejectsEmptyEgress(t *testing.T) {
	ctx := t.Context()
	s := resourceSchema(t, NewAirflowResource()).Schema

	emptyBlock, d := types.ObjectValue(airflowEgressAttrTypes(), map[string]attr.Value{
		"fqdns":      types.SetNull(types.StringType),
		"in_cluster": types.SetNull(airflowInClusterLinkType()),
		"allowlists": types.SetNull(types.StringType),
	})
	if d.HasError() {
		t.Fatalf("building the empty block: %v", d.Errors())
	}
	// A collection assigned from another resource is unknown until that
	// resource exists; judging it as absent would reject a good configuration.
	unknownBlock, d := types.ObjectValue(airflowEgressAttrTypes(), map[string]attr.Value{
		"fqdns":      types.SetUnknown(types.StringType),
		"in_cluster": types.SetNull(airflowInClusterLinkType()),
		"allowlists": types.SetNull(types.StringType),
	})
	if d.HasError() {
		t.Fatalf("building the unknown block: %v", d.Errors())
	}

	cases := []struct {
		name    string
		egress  types.Object
		wantErr bool
	}{
		{"no block", types.ObjectNull(airflowEgressAttrTypes()), false},
		{"a real grant", airflowEgressObject(t, []string{"api.example.com"}, nil, nil), false},
		{"empty block", emptyBlock, true},
		{"not yet known", unknownBlock, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := airflowNullModel()
			model.Env = types.StringValue("22222222-2222-2222-2222-222222222222")
			model.Name = types.StringValue("etl")
			model.Egress = tc.egress

			state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
			if d := state.Set(ctx, model); d.HasError() {
				t.Fatalf("building the config: %v", d.Errors())
			}
			cfg := tfsdk.Config{Schema: s, Raw: state.Raw}

			validatable, ok := NewAirflowResource().(fwresource.ResourceWithValidateConfig)
			if !ok {
				t.Fatal("the airflow resource does not implement ValidateConfig; the empty-egress check would never run")
			}
			resp := &fwresource.ValidateConfigResponse{}
			validatable.ValidateConfig(ctx, fwresource.ValidateConfigRequest{Config: cfg}, resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Errorf("error = %v, want %v (diagnostics: %v)", got, tc.wantErr, resp.Diagnostics)
			}
		})
	}
}

// ── create body: no platform default is ever materialised ─────────────────

// TestAirflowCreateBodyOmitsPlatformDefaults is the regression that stops a
// default being frozen into the environment's spec. Every field below means
// "the platform decides" when absent, and writing it at its default value would
// pin today's default for the life of the object — a later platform change
// could never reach an environment created through Terraform.
func TestAirflowCreateBodyOmitsPlatformDefaults(t *testing.T) {
	ctx := t.Context()
	plan := airflowModel{
		Name: types.StringValue("etl"),
		// Optional+Computed attributes left out of a configuration arrive as
		// unknown, exactly as they would in a real plan.
		NodeTier:         types.StringUnknown(),
		TriggererEnabled: types.BoolUnknown(),
		SleepMode:        types.BoolUnknown(),
		Tags:             types.ListUnknown(types.StringType),
		// Optional-only attributes arrive as null.
		PostgresRef:      types.StringNull(),
		DagBucketRef:     types.StringNull(),
		RuntimeImage:     types.StringNull(),
		Description:      types.StringNull(),
		TaskQuotaMaxPods: types.Int64Null(),
		Egress:           types.ObjectNull(airflowEgressAttrTypes()),
		Config:           types.MapNull(types.StringType),
	}

	body, d := airflowCreateBody(ctx, plan)
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	if body.Name != "etl" {
		t.Errorf("name = %q, want etl", body.Name)
	}
	for name, got := range map[string]any{
		"node_tier":           body.NodeTier,
		"triggerer_enabled":   body.TriggererEnabled,
		"sleep_mode":          body.SleepMode,
		"tags":                body.Tags,
		"postgres_ref":        body.PostgresRef,
		"dag_bucket_ref":      body.DagBucketRef,
		"runtime_image":       body.RuntimeImage,
		"description":         body.Description,
		"task_quota_max_pods": body.TaskQuotaMaxPods,
		"egress":              body.Egress,
		"config":              body.Config,
	} {
		if !isNilPointer(got) {
			t.Errorf("%s was sent (%#v); it must stay absent so the platform default is not frozen", name, got)
		}
	}
}

// TestAirflowCreateBodySleepModeFalseStaysAbsent covers the one field whose
// default is meaningful as a value: an awake environment is the absence of
// sleep_mode, so `sleep_mode = false` must not be written. Only "create it
// asleep" is worth sending.
func TestAirflowCreateBodySleepModeFalseStaysAbsent(t *testing.T) {
	ctx := t.Context()
	base := airflowModel{
		Name:   types.StringValue("etl"),
		Egress: types.ObjectNull(airflowEgressAttrTypes()),
		Config: types.MapNull(types.StringType),
	}

	base.SleepMode = types.BoolValue(false)
	body, _ := airflowCreateBody(ctx, base)
	if body.SleepMode != nil {
		t.Errorf("sleep_mode = %v, want absent for an explicit false", *body.SleepMode)
	}

	base.SleepMode = types.BoolValue(true)
	body, _ = airflowCreateBody(ctx, base)
	if body.SleepMode == nil || !*body.SleepMode {
		t.Error("sleep_mode = absent, want true for an environment created asleep")
	}
}

// TestAirflowCreateBodyEmptyCollectionsStayAbsent: an egress block granting
// nothing and an empty override map both mean "the baseline", which is what
// absence already means. Sending `{}` would write the key into the spec.
func TestAirflowCreateBodyEmptyCollectionsStayAbsent(t *testing.T) {
	ctx := t.Context()
	empty, d := types.ObjectValue(airflowEgressAttrTypes(), map[string]attr.Value{
		"fqdns":      types.SetNull(types.StringType),
		"in_cluster": types.SetNull(airflowInClusterLinkType()),
		"allowlists": types.SetNull(types.StringType),
	})
	if d.HasError() {
		t.Fatalf("building the empty egress object: %v", d.Errors())
	}
	emptyConfig, d := types.MapValue(types.StringType, map[string]attr.Value{})
	if d.HasError() {
		t.Fatalf("building the empty config map: %v", d.Errors())
	}

	body, d := airflowCreateBody(ctx, airflowModel{
		Name:   types.StringValue("etl"),
		Egress: empty,
		Config: emptyConfig,
	})
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	if body.Egress != nil {
		t.Errorf("egress = %#v, want absent for a block that grants nothing", body.Egress)
	}
	if body.Config != nil {
		t.Errorf("config = %#v, want absent for an empty map", body.Config)
	}
}

func TestAirflowCreateBodySendsWhatWasAskedFor(t *testing.T) {
	ctx := t.Context()
	egress := airflowEgressObject(t, []string{"api.example.com"},
		[]airflowInClusterLinkModel{{Kind: types.StringValue("ManagedPostgreSQL"), Name: types.StringValue("warehouse")}},
		[]string{"shared-apis"})
	config, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"core.parallelism": "64"})
	if d.HasError() {
		t.Fatalf("building the config map: %v", d.Errors())
	}
	tags, d := types.ListValueFrom(ctx, types.StringType, []string{"prod"})
	if d.HasError() {
		t.Fatalf("building the tag list: %v", d.Errors())
	}

	body, d := airflowCreateBody(ctx, airflowModel{
		Name:             types.StringValue("etl"),
		PostgresRef:      types.StringValue("warehouse"),
		DagBucketRef:     types.StringValue("etl-dags"),
		NodeTier:         types.StringValue("medium"),
		TriggererEnabled: types.BoolValue(false),
		RuntimeImage:     types.StringValue("registry.example.com/airflow:3.1-etl"),
		TaskQuotaMaxPods: types.Int64Value(40),
		Description:      types.StringValue("nightly load"),
		Tags:             tags,
		Egress:           egress,
		Config:           config,
	})
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}

	if body.NodeTier == nil || *body.NodeTier != console.AirflowNodeTier("medium") {
		t.Errorf("node_tier = %v, want medium", body.NodeTier)
	}
	if body.TriggererEnabled == nil || *body.TriggererEnabled {
		t.Error("an explicit triggerer_enabled = false must be sent: it is a choice, not a default")
	}
	if body.PostgresRef == nil || *body.PostgresRef != "warehouse" {
		t.Errorf("postgres_ref = %v", body.PostgresRef)
	}
	if body.TaskQuotaMaxPods == nil || *body.TaskQuotaMaxPods != 40 {
		t.Errorf("task_quota_max_pods = %v, want 40", body.TaskQuotaMaxPods)
	}
	if body.Egress == nil || body.Egress.Fqdns == nil || (*body.Egress.Fqdns)[0] != "api.example.com" {
		t.Errorf("egress.fqdns = %#v", body.Egress)
	}
	if body.Egress == nil || body.Egress.InCluster == nil || (*body.Egress.InCluster)[0].Kind != console.AirflowLinkKindManagedPostgreSQL {
		t.Errorf("egress.in_cluster = %#v", body.Egress)
	}
	if body.Config == nil || (*body.Config)["core.parallelism"] != "64" {
		t.Errorf("config = %#v", body.Config)
	}
}

// ── patch body: clear is distinct from unchanged ──────────────────────────

func TestAirflowPatchBodyUnchangedSendsNothing(t *testing.T) {
	ctx := t.Context()
	state := airflowFullModel(t)
	body, d := airflowPatchBody(ctx, state, state)
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	for name, got := range map[string]any{
		"node_tier":           body.NodeTier,
		"triggerer_enabled":   body.TriggererEnabled,
		"sleep_mode":          body.SleepMode,
		"runtime_image":       body.RuntimeImage,
		"description":         body.Description,
		"task_quota_max_pods": body.TaskQuotaMaxPods,
		"tags":                body.Tags,
		"egress":              body.Egress,
		"config":              body.Config,
	} {
		if !isNilPointer(got) {
			t.Errorf("%s = %#v, want absent: nothing changed, so nothing should be patched", name, got)
		}
	}
}

// TestAirflowPatchBodyClears is the other half of the contract: for these four
// fields "omitted" means unchanged, so a removed attribute has to be spelled
// out with the sentinel the API reads as "back to the platform default".
func TestAirflowPatchBodyClears(t *testing.T) {
	ctx := t.Context()
	state := airflowFullModel(t)
	plan := state
	plan.RuntimeImage = types.StringNull()
	plan.Description = types.StringNull()
	plan.TaskQuotaMaxPods = types.Int64Null()
	plan.Egress = types.ObjectNull(airflowEgressAttrTypes())
	plan.Config = types.MapNull(types.StringType)

	body, d := airflowPatchBody(ctx, plan, state)
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	// An empty string, not a nil pointer: nil would be read as "leave the pin
	// alone" and the environment would stay on the old image for ever.
	if body.RuntimeImage == nil || *body.RuntimeImage != "" {
		t.Errorf("runtime_image = %v, want a pointer to \"\" to clear the pin", body.RuntimeImage)
	}
	if body.Description == nil || *body.Description != "" {
		t.Errorf("description = %v, want a pointer to \"\" to clear it", body.Description)
	}
	// Zero is how the API spells "no ceiling of your own".
	if body.TaskQuotaMaxPods == nil || *body.TaskQuotaMaxPods != 0 {
		t.Errorf("task_quota_max_pods = %v, want a pointer to 0 to clear the quota", body.TaskQuotaMaxPods)
	}
	// An empty object / map, not an absent one: absent means unchanged.
	if body.Egress == nil {
		t.Fatal("egress = absent, want an empty object to revoke the grants")
	}
	if !airflowEgressIsEmpty(body.Egress) {
		t.Errorf("egress = %#v, want an empty object", body.Egress)
	}
	if body.Config == nil {
		t.Fatal("config = absent, want an empty map to restore the defaults")
	}
	if len(*body.Config) != 0 {
		t.Errorf("config = %#v, want an empty map", body.Config)
	}
}

func TestAirflowPatchBodySendsChangedValues(t *testing.T) {
	ctx := t.Context()
	state := airflowFullModel(t)
	plan := state
	plan.NodeTier = types.StringValue("large")
	plan.RuntimeImage = types.StringValue("registry.example.com/airflow:3.1-new")
	plan.TaskQuotaMaxPods = types.Int64Value(80)
	plan.Egress = airflowEgressObject(t, []string{"other.example.com"}, nil, nil)

	body, d := airflowPatchBody(ctx, plan, state)
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	if body.NodeTier == nil || *body.NodeTier != console.AirflowNodeTier("large") {
		t.Errorf("node_tier = %v, want large", body.NodeTier)
	}
	if body.RuntimeImage == nil || *body.RuntimeImage != "registry.example.com/airflow:3.1-new" {
		t.Errorf("runtime_image = %v", body.RuntimeImage)
	}
	if body.TaskQuotaMaxPods == nil || *body.TaskQuotaMaxPods != 80 {
		t.Errorf("task_quota_max_pods = %v, want 80", body.TaskQuotaMaxPods)
	}
	// Replace, not merge: the old fqdn and the in-cluster grant are simply gone.
	if body.Egress == nil || body.Egress.Fqdns == nil || len(*body.Egress.Fqdns) != 1 || (*body.Egress.Fqdns)[0] != "other.example.com" {
		t.Errorf("egress.fqdns = %#v, want exactly the new set", body.Egress)
	}
	if body.Egress != nil && body.Egress.InCluster != nil {
		t.Errorf("egress.in_cluster = %#v, want absent: the new block does not name any", body.Egress.InCluster)
	}
}

// TestAirflowPatchBodySleepModeFalseIsSent is the difference between create and
// update: on a PATCH `false` is the user asking to wake the environment up, so
// unlike on create it must be sent.
func TestAirflowPatchBodySleepModeFalseIsSent(t *testing.T) {
	ctx := t.Context()
	state := airflowFullModel(t)
	state.SleepMode = types.BoolValue(true)
	plan := state
	plan.SleepMode = types.BoolValue(false)

	body, _ := airflowPatchBody(ctx, plan, state)
	if body.SleepMode == nil {
		t.Fatal("sleep_mode = absent, want false: waking an environment up is an explicit request")
	}
	if *body.SleepMode {
		t.Error("sleep_mode = true, want false")
	}
}

// ── node tier recovery ────────────────────────────────────────────────────

// TestAirflowNodeTierFromResources covers the read-mapping the API forces on
// us: node_tier goes in on a write and never comes back, only the cpu/memory it
// resolved into.
func TestAirflowNodeTierFromResources(t *testing.T) {
	fallback := types.StringValue("small")

	for tier, want := range airflowTierResources {
		got := airflowNodeTierFromResources(&want.cpuRequest, &want.cpuLimit, &want.memory, types.StringNull())
		if got.ValueString() != tier {
			t.Errorf("tier %s resolved back to %q", tier, got.ValueString())
		}
	}

	// A catalogue that has moved on must degrade to "unchanged", never to a
	// wrong tier and never to a blank one.
	odd := "3141m"
	if got := airflowNodeTierFromResources(&odd, &odd, &odd, fallback); !got.Equal(fallback) {
		t.Errorf("unrecognised resources gave %q, want the fallback", got.ValueString())
	}
	if got := airflowNodeTierFromResources(nil, nil, nil, fallback); !got.Equal(fallback) {
		t.Errorf("absent resources gave %q, want the fallback", got.ValueString())
	}
	// An unknown can never reach state, so it is not a usable fallback.
	if got := airflowNodeTierFromResources(&odd, &odd, &odd, types.StringUnknown()); !got.IsNull() {
		t.Errorf("unknown fallback gave %#v, want null", got)
	}
}

// ── egress round trip ─────────────────────────────────────────────────────

// TestAirflowEgressRoundTrip: what is written has to read back identical, or
// every plan after the first reports drift on the grants.
func TestAirflowEgressRoundTrip(t *testing.T) {
	ctx := t.Context()
	obj := airflowEgressObject(t,
		[]string{"api.example.com", "*.cdn.example.org"},
		[]airflowInClusterLinkModel{
			{Kind: types.StringValue("ManagedPostgreSQL"), Name: types.StringValue("warehouse")},
			{Kind: types.StringValue("HfKeyValueCache"), Name: types.StringValue("cache")},
		},
		[]string{"shared-apis"})

	wire, d := airflowEgressFromObject(ctx, obj)
	if d.HasError() {
		t.Fatalf("to wire: %v", d.Errors())
	}
	back, d := airflowEgressToObject(ctx, wire)
	if d.HasError() {
		t.Fatalf("from wire: %v", d.Errors())
	}
	if !back.Equal(obj) {
		t.Errorf("egress did not round-trip:\n got %v\nwant %v", back, obj)
	}
}

// TestAirflowEgressToObjectNullsEmptyCollections: the API always serialises all
// three collections, empty ones included, so an absent grant has to map back to
// null rather than to `[]` — otherwise a block naming only `fqdns` would plan a
// change on the other two on every run.
func TestAirflowEgressToObjectNullsEmptyCollections(t *testing.T) {
	ctx := t.Context()
	fqdns := []string{"api.example.com"}
	none := []string{}
	noLinks := []console.AirflowInClusterLinkRequest{}

	obj, d := airflowEgressToObject(ctx, &console.AirflowEgressRequest{
		Fqdns:      &fqdns,
		Allowlists: &none,
		InCluster:  &noLinks,
	})
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	var model airflowEgressModel
	if d := obj.As(ctx, &model, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatalf("reading the object back: %v", d.Errors())
	}
	if model.Fqdns.IsNull() {
		t.Error("fqdns = null, want the one grant that was set")
	}
	if !model.Allowlists.IsNull() {
		t.Errorf("allowlists = %v, want null for an empty collection", model.Allowlists)
	}
	if !model.InCluster.IsNull() {
		t.Errorf("in_cluster = %v, want null for an empty collection", model.InCluster)
	}

	// Nothing granted at all is the same as no block.
	empty, d := airflowEgressToObject(ctx, &console.AirflowEgressRequest{Fqdns: &none, Allowlists: &none, InCluster: &noLinks})
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	if !empty.IsNull() {
		t.Errorf("an egress granting nothing mapped to %v, want a null object", empty)
	}
	absent, _ := airflowEgressToObject(ctx, nil)
	if !absent.IsNull() {
		t.Errorf("an absent egress mapped to %v, want a null object", absent)
	}
}

func TestAirflowConfigRoundTrip(t *testing.T) {
	ctx := t.Context()
	in := map[string]string{"core.parallelism": "64", "scheduler.min_file_process_interval": "30"}

	m, d := airflowConfigToMap(ctx, &in)
	if d.HasError() {
		t.Fatalf("to map: %v", d.Errors())
	}
	out, d := airflowConfigFromMap(ctx, m)
	if d.HasError() {
		t.Fatalf("from map: %v", d.Errors())
	}
	if len(out) != len(in) {
		t.Fatalf("config = %#v, want %#v", out, in)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("config[%q] = %q, want %q", k, out[k], v)
		}
	}

	// `None` on the wire means "no overrides", which is a null map and not an
	// empty one — an empty map in state would plan a clear on every run.
	empty := map[string]string{}
	if got, _ := airflowConfigToMap(ctx, &empty); !got.IsNull() {
		t.Errorf("an empty override map mapped to %v, want null", got)
	}
	if got, _ := airflowConfigToMap(ctx, nil); !got.IsNull() {
		t.Errorf("absent overrides mapped to %v, want null", got)
	}
}

// ── validator patterns ────────────────────────────────────────────────────

// TestAirflowFqdnPattern mirrors the platform's own fqdn rule. It has to reject
// anything the API would normalise (uppercase, surrounding space) rather than
// merely anything it would refuse: a value that came back normalised would
// differ from the plan and fail the apply as an inconsistent result.
func TestAirflowFqdnPattern(t *testing.T) {
	for _, ok := range []string{"example.com", "api.example.com", "*.example.org", "*.api.example.org", "a1-b.example.com"} {
		if !airflowFqdnPattern.MatchString(ok) {
			t.Errorf("%q rejected, want accepted", ok)
		}
	}
	for _, bad := range []string{
		"Example.com",       // would be lowercased by the API
		" example.com",      // would be trimmed by the API
		"example.com ",      // ditto
		"example",           // single label
		"*",                 // bare wildcard
		"*.com",             // wildcard over a whole TLD
		"api.*.example.com", // wildcard anywhere but in front
		"10.0.0.1",          // ip literal
		"-bad.example.com",  // label starting with a hyphen
		"bad-.example.com",  // label ending with a hyphen
		"",
	} {
		if airflowFqdnPattern.MatchString(bad) {
			t.Errorf("%q accepted, want rejected", bad)
		}
	}
}

func TestAirflowConfigKeyPattern(t *testing.T) {
	for _, ok := range []string{"core.parallelism", "scheduler.min_file_process_interval", "c1.k2"} {
		if !airflowConfigKeyPattern.MatchString(ok) {
			t.Errorf("%q rejected, want accepted", ok)
		}
	}
	for _, bad := range []string{
		"CORE.PARALLELISM", // refused, not folded, so the stored key is canonical
		"parallelism",      // no section
		"core.",            // empty key
		".parallelism",     // empty section
		"core.sub.key",     // one dot only
		"1core.parallelism",
		"core.1parallelism",
		"core-x.parallelism",
	} {
		if airflowConfigKeyPattern.MatchString(bad) {
			t.Errorf("%q accepted, want rejected", bad)
		}
	}
}

func TestAirflowDNSLabelPattern(t *testing.T) {
	for _, ok := range []string{"warehouse", "a", "a-b-1", "x1"} {
		if !airflowDNSLabelPattern.MatchString(ok) {
			t.Errorf("%q rejected, want accepted", ok)
		}
	}
	for _, bad := range []string{"Warehouse", "-warehouse", "warehouse-", "ware_house", ""} {
		if airflowDNSLabelPattern.MatchString(bad) {
			t.Errorf("%q accepted, want rejected", bad)
		}
	}
}

// TestAirflowNodeTierCatalogueHasNoNano guards the one tier the platform
// removed on purpose: 512Mi OOM-kills an Airflow 3 triggerer, so an
// environment created at that size crash-looped from birth.
func TestAirflowNodeTierCatalogueHasNoNano(t *testing.T) {
	for _, tier := range airflowNodeTiers {
		if tier == "nano" {
			t.Fatal("nano is not a valid Airflow tier: it cannot hold the triggerer")
		}
		if _, ok := airflowTierResources[tier]; !ok {
			t.Errorf("tier %q has no resolved resources, so it could never be read back", tier)
		}
	}
	if len(airflowNodeTiers) != len(airflowTierResources) {
		t.Errorf("the accepted tiers (%d) and the resource table (%d) have drifted apart",
			len(airflowNodeTiers), len(airflowTierResources))
	}
	for _, tier := range airflowNodeTiers {
		if !console.AirflowNodeTier(tier).Valid() {
			t.Errorf("tier %q is not a value the API accepts", tier)
		}
	}
}

func TestAirflowLinkKindsAreWireValues(t *testing.T) {
	for _, kind := range airflowLinkKinds {
		if !console.AirflowLinkKind(kind).Valid() {
			t.Errorf("link kind %q is not a value the API accepts", kind)
		}
	}
	if len(airflowLinkKinds) != 4 {
		t.Errorf("expected the four linkable kinds, got %v", airflowLinkKinds)
	}
}

// ── acceptance tests (skipped without credentials) ────────────────────────

// TestAccAirflowResource covers create → read → update (tier + egress) →
// import → destroy. Skipped unless HYPERFLUID_CREDENTIALS is set.
func TestAccAirflowResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAirflowConfig("micro", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_airflow.etl", "name", "tf-acc-af"),
					resource.TestCheckResourceAttr("hyperfluid_airflow.etl", "node_tier", "micro"),
					// Omitted from the config: asserts the platform default is
					// read back rather than a value the provider invented.
					resource.TestCheckResourceAttr("hyperfluid_airflow.etl", "triggerer_enabled", "true"),
					resource.TestCheckResourceAttr("hyperfluid_airflow.etl", "sleep_mode", "false"),
					resource.TestCheckNoResourceAttr("hyperfluid_airflow.etl", "runtime_image"),
					resource.TestCheckNoResourceAttr("hyperfluid_airflow.etl", "task_quota_max_pods"),
					resource.TestCheckResourceAttr("hyperfluid_airflow.etl", "phase", "Running"),
					resource.TestCheckResourceAttrSet("hyperfluid_airflow.etl", "dag_bucket"),
					resource.TestCheckResourceAttrSet("hyperfluid_airflow.etl", "task_namespace"),
					resource.TestCheckResourceAttrSet("hyperfluid_airflow.etl", "memory_limit"),
				),
			},
			{
				// Tier change plus an egress grant, both applied in place.
				Config: testAccAirflowConfig("small", `
  egress = {
    fqdns = ["api.example.com"]
  }
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_airflow.etl", "node_tier", "small"),
					resource.TestCheckResourceAttr("hyperfluid_airflow.etl", "egress.fqdns.#", "1"),
				),
			},
			{
				// Removing the block revokes the grants rather than keeping them.
				Config: testAccAirflowConfig("small", ""),
				Check:  resource.TestCheckNoResourceAttr("hyperfluid_airflow.etl", "egress"),
			},
			{
				ResourceName:      "hyperfluid_airflow.etl",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccAirflowConfig(tier, extra string) string {
	return `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_airflow" "etl" {
  env       = data.hyperfluid_env.default.id
  name      = "tf-acc-af"
  node_tier = "` + tier + `"
` + extra + `}
`
}
