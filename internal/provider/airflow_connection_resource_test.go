// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── test helpers ──────────────────────────────────────────────────────────

func airflowConnectionNullModel() airflowConnectionModel {
	return airflowConnectionModel{
		Conditions: types.ListNull(airflowConnectionConditionType()),
	}
}

// ── schema ────────────────────────────────────────────────────────────────

func TestAirflowConnectionResourceSchema(t *testing.T) {
	ctx := t.Context()
	s := resourceSchema(t, NewAirflowConnectionResource()).Schema

	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if d := state.Set(ctx, airflowConnectionNullModel()); d.HasError() {
		t.Fatalf("airflowConnectionModel does not fit the resource schema: %v", d.Errors())
	}

	// permission_level must stay Optional-only. Computed would make an absent
	// pin indistinguishable from a pinned "viewer", and absent is a real state:
	// the platform re-resolves its default on every reconcile, so an unpinned
	// connection follows a default that may later be narrowed.
	level, ok := s.Attributes["permission_level"]
	if !ok {
		t.Fatal("permission_level attribute missing")
	}
	if !level.IsOptional() || level.IsComputed() {
		t.Errorf("permission_level must be Optional and NOT Computed, got optional=%v computed=%v",
			level.IsOptional(), level.IsComputed())
	}

	// Neither target may be Computed either: exactly one of the two is named by
	// the configuration and the pair is what decides the connection's type.
	for _, name := range []string{"managed_postgresql_ref", "bucket_ref"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("%s attribute missing", name)
		}
		if !attr.IsOptional() || attr.IsComputed() {
			t.Errorf("%s must be Optional and not Computed", name)
		}
	}

	// The s3-session route mints short-lived in-cluster credentials and is
	// deliberately not reachable from Terraform. Assert no attribute smells
	// like one, so a later edit cannot quietly add one.
	for name := range s.Attributes {
		for _, banned := range []string{"secret", "session", "access_key", "token", "password"} {
			if strings.Contains(name, banned) {
				t.Errorf("attribute %q exposes a credential; object-store sessions must not reach state", name)
			}
		}
	}
}

// ── exactly-one-target, enforced at plan time ─────────────────────────────

// TestAirflowConnectionExactlyOneTarget runs the resource's own
// ConfigValidators, which is what makes the one-target rule a plan error rather
// than a 400 discovered halfway through an apply.
func TestAirflowConnectionExactlyOneTarget(t *testing.T) {
	ctx := t.Context()
	s := resourceSchema(t, NewAirflowConnectionResource()).Schema
	validatable, ok := NewAirflowConnectionResource().(fwresource.ResourceWithConfigValidators)
	if !ok {
		t.Fatal("the connection resource declares no ConfigValidators; the one-target rule would only be enforced by the API")
	}
	validators := validatable.ConfigValidators(ctx)
	if len(validators) == 0 {
		t.Fatal("no config validators declared; the one-target rule would only be enforced by the API")
	}

	cases := []struct {
		name    string
		pg      types.String
		bucket  types.String
		wantErr bool
	}{
		{"postgres only", types.StringValue("warehouse"), types.StringNull(), false},
		{"bucket only", types.StringNull(), types.StringValue("lake"), false},
		{"both", types.StringValue("warehouse"), types.StringValue("lake"), true},
		{"neither", types.StringNull(), types.StringNull(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := airflowConnectionNullModel()
			model.Airflow = types.StringValue("11111111-1111-1111-1111-111111111111")
			model.ConnID = types.StringValue("warehouse")
			model.ManagedPostgresqlRef = tc.pg
			model.BucketRef = tc.bucket

			state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
			if d := state.Set(ctx, model); d.HasError() {
				t.Fatalf("building the config: %v", d.Errors())
			}
			cfg := tfsdk.Config{Schema: s, Raw: state.Raw}

			resp := &fwresource.ValidateConfigResponse{}
			for _, v := range validators {
				v.ValidateResource(ctx, fwresource.ValidateConfigRequest{Config: cfg}, resp)
			}
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Errorf("error = %v, want %v (diagnostics: %v)", got, tc.wantErr, resp.Diagnostics)
			}
		})
	}
}

// ── un-pinning forces replacement ─────────────────────────────────────────

// TestAirflowConnectionUnpinRequiresReplace: the API reads permission_level
// only when it is present, so there is no PATCH that un-pins a level. Removing
// the attribute has to plan a replacement, or state would claim the platform
// default was back while yesterday's pin stayed in force.
func TestAirflowConnectionUnpinRequiresReplace(t *testing.T) {
	cases := []struct {
		name         string
		state        types.String
		config       types.String
		wantsReplace bool
	}{
		{"pin removed", types.StringValue("editor"), types.StringNull(), true},
		{"pin changed", types.StringValue("viewer"), types.StringValue("editor"), false},
		{"pin added", types.StringNull(), types.StringValue("editor"), false},
		{"pin unchanged", types.StringValue("editor"), types.StringValue("editor"), false},
		{"never pinned", types.StringNull(), types.StringNull(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := airflowConnectionUnpinRequiresReplace(tc.state, tc.config); got != tc.wantsReplace {
				t.Errorf("requiresReplace = %v, want %v", got, tc.wantsReplace)
			}
		})
	}
}

// ── model mapping ─────────────────────────────────────────────────────────

// TestAirflowConnectionToModel covers the two identifiers that are easy to
// confuse: the API addresses a connection by its object `name`, while a DAG —
// and this resource's configuration — names it by `conn_id`.
func TestAirflowConnectionToModel(t *testing.T) {
	ctx := t.Context()
	airflowID := "11111111-1111-1111-1111-111111111111"
	applied := true
	reason := "Applied"
	message := "credential written"

	model, d := airflowConnectionToModel(ctx, airflowID, &console.AirflowConnectionResponse{
		Name:                    "etl-warehouse-3f2a",
		ConnId:                  "warehouse",
		ConnectionType:          "postgres",
		ManagedPostgresqlRef:    ptr("warehouse"),
		PermissionLevel:         ptr("editor"),
		ResolvedPermissionLevel: "editor",
		Phase:                   "Ready",
		SpecObserved:            true,
		SourceApplied:           &applied,
		Conditions: []console.AirflowConnectionConditionResponse{
			{Type: "Ready", Status: "True", Reason: &reason, Message: &message},
		},
	})
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}

	if got := model.ID.ValueString(); got != airflowID+"/etl-warehouse-3f2a" {
		t.Errorf("id = %q, want the <airflow>/<name> composite", got)
	}
	if model.Name.ValueString() == model.ConnID.ValueString() {
		t.Error("name and conn_id collapsed into one value; they are different identifiers")
	}
	if got := model.PermissionLevel.ValueString(); got != "editor" {
		t.Errorf("permission_level = %q, want the pinned level", got)
	}
	if !model.SourceApplied.ValueBool() {
		t.Error("source_applied = false, want true")
	}
	if model.Conditions.IsNull() || len(model.Conditions.Elements()) != 1 {
		t.Errorf("conditions = %v, want the one reported condition", model.Conditions)
	}
}

// TestAirflowConnectionToModelKeepsAnAbsentPinAbsent is the other half of the
// permission_level contract: nothing pinned must read back as null, never as
// the level the platform happens to resolve today.
func TestAirflowConnectionToModelKeepsAnAbsentPinAbsent(t *testing.T) {
	ctx := t.Context()
	model, d := airflowConnectionToModel(ctx, "11111111-1111-1111-1111-111111111111", &console.AirflowConnectionResponse{
		Name:                    "etl-lake-9c11",
		ConnId:                  "lake",
		ConnectionType:          "bucket",
		BucketRef:               ptr("lake"),
		PermissionLevel:         nil,
		ResolvedPermissionLevel: "viewer",
		Phase:                   "Ready",
		SpecObserved:            true,
		Conditions:              []console.AirflowConnectionConditionResponse{},
	})
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}
	if !model.PermissionLevel.IsNull() {
		t.Errorf("permission_level = %v, want null: the user pinned nothing", model.PermissionLevel)
	}
	if got := model.ResolvedPermissionLevel.ValueString(); got != "viewer" {
		t.Errorf("resolved_permission_level = %q, want the level actually in force", got)
	}
	// source_applied is optional on the wire; an absent value is not "applied".
	if model.SourceApplied.ValueBool() {
		t.Error("source_applied = true for an absent value, want false")
	}
	if model.Conditions.IsNull() {
		t.Error("conditions = null for an empty list, want an empty list")
	}
}

// ── failure detail ────────────────────────────────────────────────────────

// TestAirflowConnectionDetail: when a connection will not apply, the reason is
// in its conditions, and that reason is the whole value of the diagnostic.
func TestAirflowConnectionDetail(t *testing.T) {
	reason := "TargetMissing"
	message := "no ManagedPostgreSQL named warehouse in this harbor"
	conn := &console.AirflowConnectionResponse{
		SpecObserved: true,
		Conditions: []console.AirflowConnectionConditionResponse{
			{Type: "OwnershipEstablished", Status: "True"},
			{Type: "SourceResolved", Status: "False", Reason: &reason, Message: &message},
		},
	}
	got := airflowConnectionDetail(conn)
	if !strings.Contains(got, message) {
		t.Errorf("detail = %q, want it to carry the condition message", got)
	}
	if !strings.Contains(got, reason) {
		t.Errorf("detail = %q, want it to carry the condition reason", got)
	}

	// Not looked at yet is a different answer from looked at and unsatisfiable,
	// and saying so is the point of spec_observed.
	unobserved := &console.AirflowConnectionResponse{SpecObserved: false}
	if got := airflowConnectionDetail(unobserved); !strings.Contains(got, "not observed") {
		t.Errorf("detail = %q, want it to say the declaration has not been observed", got)
	}

	// Everything satisfied and nothing to report still has to produce a
	// sentence rather than an empty string in the middle of an error message.
	healthy := &console.AirflowConnectionResponse{
		SpecObserved: true,
		Conditions:   []console.AirflowConnectionConditionResponse{{Type: "Ready", Status: "True"}},
	}
	if got := airflowConnectionDetail(healthy); got == "" {
		t.Error("detail = \"\", want a statement of what is known")
	}
}

// ── the conn_id the platform hands back ───────────────────────────────────

// TestAirflowConnectionExistsDiagnostic: a conn_id already held by a managed
// connection is refused before anything is written, because the create route
// would not refuse it — it would allocate `warehouse-1`. The message has to
// name the id, what already holds it, and the way out (import).
func TestAirflowConnectionExistsDiagnostic(t *testing.T) {
	airflowID := "11111111-1111-1111-1111-111111111111"
	summary, detail := airflowConnectionExistsDiagnostic("warehouse", airflowID, &console.AirflowConnectionResponse{
		Name:           "afconn-3f2a",
		ConnId:         "warehouse",
		ConnectionType: "postgres",
	})
	if summary == "" {
		t.Error("summary is empty")
	}
	for _, want := range []string{"warehouse", "afconn-3f2a", "postgres", "terraform import", airflowID + "/warehouse"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to mention %q", detail, want)
		}
	}
	// It must say what would otherwise happen, or the user has no reason to
	// believe the create was worth refusing.
	if !strings.Contains(detail, "warehouse-1") {
		t.Errorf("detail = %q, want it to name the id the platform would have allocated", detail)
	}

	// An unknown type must not produce a dangling parenthesis or the word
	// "<nil>" in the middle of a diagnostic.
	_, bare := airflowConnectionExistsDiagnostic("lake", airflowID, &console.AirflowConnectionResponse{Name: "afconn-9c11"})
	if strings.Contains(bare, "a  connection") || strings.Contains(bare, "%!") {
		t.Errorf("detail = %q, malformed with no connection type", bare)
	}
}

// TestAirflowConnectionRenamedDiagnostic: losing the race means the POST came
// back under a suffixed conn_id. That object is deleted again rather than
// stored, and the diagnostic must name all three things — what was asked for,
// what was allocated, and what was removed — or the user cannot tell whether a
// credential was left behind.
func TestAirflowConnectionRenamedDiagnostic(t *testing.T) {
	summary, detail := airflowConnectionRenamedDiagnostic("warehouse", "warehouse-1", "afconn-77bd", nil)
	if summary == "" {
		t.Error("summary is empty")
	}
	for _, want := range []string{"warehouse", "warehouse-1", "afconn-77bd"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to mention %q", detail, want)
		}
	}
	if !strings.Contains(detail, "state") {
		t.Errorf("detail = %q, want it to say nothing was written to state", detail)
	}
	if !strings.Contains(detail, "deleted") {
		t.Errorf("detail = %q, want it to say the unwanted connection was removed", detail)
	}

	// The one thing this message must not get wrong: when handing the
	// connection back failed, it is still live and the user has to remove it.
	// Claiming it was deleted anyway would leave a credential nobody knows about.
	_, stuck := airflowConnectionRenamedDiagnostic("warehouse", "warehouse-1", "afconn-77bd",
		errors.New("403 forbidden"))
	if strings.Contains(stuck, "has been deleted") {
		t.Errorf("detail = %q, claims a delete that failed", stuck)
	}
	for _, want := range []string{"403 forbidden", "still live", "hfctl airflow connections delete"} {
		if !strings.Contains(stuck, want) {
			t.Errorf("detail = %q, want it to mention %q", stuck, want)
		}
	}
}

func TestAirflowConnectionCollisionSuffix(t *testing.T) {
	existing := "postgres"
	if got := airflowConnectionCollisionSuffix(&console.AirflowConnectionResponse{
		CollisionExistingConnectionType: &existing,
	}); !strings.Contains(got, "postgres") {
		t.Errorf("suffix = %q, want it to name the colliding connection's type", got)
	}
	if got := airflowConnectionCollisionSuffix(&console.AirflowConnectionResponse{}); got != "" {
		t.Errorf("suffix = %q, want empty when the type is unknown", got)
	}
}

func TestAirflowPermissionLevelsAreWireValues(t *testing.T) {
	for _, level := range airflowPermissionLevels {
		if !console.PermissionLevel(level).Valid() {
			t.Errorf("permission level %q is not a value the API accepts", level)
		}
	}
	if len(airflowPermissionLevels) != 2 {
		t.Errorf("expected exactly viewer and editor, got %v", airflowPermissionLevels)
	}
}

// ── acceptance tests (skipped without credentials) ────────────────────────

// TestAccAirflowConnectionResource covers a Postgres connection and a bucket
// one, an in-place permission change, the data-source lookup by conn_id, and
// import. Skipped unless HYPERFLUID_CREDENTIALS is set.
func TestAccAirflowConnectionResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAirflowConnectionConfig(`permission_level = "viewer"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.warehouse", "conn_id", "tf_acc_warehouse"),
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.warehouse", "permission_level", "viewer"),
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.warehouse", "resolved_permission_level", "viewer"),
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.warehouse", "phase", "Ready"),
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.warehouse", "collision", "false"),
					// The object name is derived and is not the conn_id.
					resource.TestCheckResourceAttrSet("hyperfluid_airflow_connection.warehouse", "name"),

					// No pin: absent stays absent, and the resolved level says
					// what the platform default actually granted.
					resource.TestCheckNoResourceAttr("hyperfluid_airflow_connection.lake", "permission_level"),
					resource.TestCheckResourceAttrSet("hyperfluid_airflow_connection.lake", "resolved_permission_level"),
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.lake", "connection_type", "bucket"),

					resource.TestCheckResourceAttrPair(
						"data.hyperfluid_airflow_connection.by_conn_id", "name",
						"hyperfluid_airflow_connection.warehouse", "name"),
				),
			},
			{
				// Raising a pinned level is applied in place.
				Config: testAccAirflowConnectionConfig(`permission_level = "editor"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.warehouse", "permission_level", "editor"),
					resource.TestCheckResourceAttr("hyperfluid_airflow_connection.warehouse", "resolved_permission_level", "editor"),
				),
			},
			{
				ResourceName:      "hyperfluid_airflow_connection.warehouse",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					env := s.RootModule().Resources["hyperfluid_airflow.etl"]
					conn := s.RootModule().Resources["hyperfluid_airflow_connection.warehouse"]
					return env.Primary.ID + "/" + conn.Primary.Attributes["conn_id"], nil
				},
			},
		},
	})
}

// TestAccAirflowConnectionRejectsTwoTargets asserts the one-target rule lands
// at plan time, before anything is created.
func TestAccAirflowConnectionRejectsTwoTargets(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "hyperfluid_airflow_connection" "bad" {
  airflow                = "11111111-1111-1111-1111-111111111111"
  conn_id                = "tf_acc_both"
  managed_postgresql_ref = "warehouse"
  bucket_ref             = "lake"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
		},
	})
}

func testAccAirflowConnectionConfig(level string) string {
	return `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_managed_postgresql" "warehouse" {
  env              = data.hyperfluid_env.default.id
  name             = "tf-acc-af-pg"
  database_name    = "warehouse"
  engine           = "postgresql"
  version          = "17"
  node_tier        = "nano"
  storage_capacity = 1
}

resource "hyperfluid_bucket" "lake" {
  env  = data.hyperfluid_env.default.id
  name = "tf-acc-af-lake"
}

resource "hyperfluid_airflow" "etl" {
  env       = data.hyperfluid_env.default.id
  name      = "tf-acc-af-conn"
  node_tier = "micro"
}

resource "hyperfluid_airflow_connection" "warehouse" {
  airflow                = hyperfluid_airflow.etl.id
  conn_id                = "tf_acc_warehouse"
  managed_postgresql_ref = hyperfluid_managed_postgresql.warehouse.slug
  ` + level + `
}

resource "hyperfluid_airflow_connection" "lake" {
  airflow    = hyperfluid_airflow.etl.id
  conn_id    = "tf_acc_lake"
  bucket_ref = hyperfluid_bucket.lake.name
}

data "hyperfluid_airflow_connection" "by_conn_id" {
  airflow    = hyperfluid_airflow.etl.id
  conn_id    = hyperfluid_airflow_connection.warehouse.conn_id
  depends_on = [hyperfluid_airflow_connection.warehouse]
}
`
}
