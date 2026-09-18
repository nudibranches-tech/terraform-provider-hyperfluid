// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
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

	if got := model.ID.ValueString(); got != airflowID+"/warehouse" {
		t.Errorf("id = %q, want the <airflow>/<conn_id> composite, which is the import id", got)
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
	for _, want := range []string{"403 forbidden", "may still be live", "hfctl airflow connections delete"} {
		if !strings.Contains(stuck, want) {
			t.Errorf("detail = %q, want it to mention %q", stuck, want)
		}
	}

	// A delete the API accepted but that never converged reaches this message
	// through the same argument, and must read the same way: the object hangs
	// on the finalizer that releases its credential, so "revoked" would be a
	// claim nobody checked.
	_, unconfirmed := airflowConnectionRenamedDiagnostic("warehouse", "warehouse-1", "afconn-77bd",
		errors.New("the delete was accepted but the connection is still present: timed out"))
	if strings.Contains(unconfirmed, "has been deleted") || strings.Contains(unconfirmed, "is revoked as that object") {
		t.Errorf("detail = %q, claims a revocation that was never confirmed", unconfirmed)
	}
	if !strings.Contains(unconfirmed, "may still be live") {
		t.Errorf("detail = %q, want it to say the connection may still be live", unconfirmed)
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
				// No ImportStateIdFunc: the default is the resource's own `id`,
				// which is the habitual source of an import id and now the
				// composite ImportState parses. Rebuilding the id here would
				// paper over exactly the mismatch this step has to catch.
				ResourceName:      "hyperfluid_airflow_connection.warehouse",
				ImportState:       true,
				ImportStateVerify: true,
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

// ── the wait's settle predicate ───────────────────────────────────────────

// TestAirflowConnectionSettled is the regression for a wait that was a no-op
// after an Update. A PATCH bumps the object's generation, and the projection
// read straight afterwards still describes the previous one: its `phase` can
// say `Ready` about a credential that has not been re-minted. Nothing may
// settle — and nothing may abort — on a projection the platform has not
// observed.
func TestAirflowConnectionSettled(t *testing.T) {
	cases := []struct {
		name        string
		phase       string
		observed    bool
		wantSettled bool
		wantErr     bool
	}{
		{"ready and observed", airflowConnectionPhaseReady, true, true, false},
		// The finding itself: `Ready` for the generation before the PATCH.
		{"ready but unobserved", airflowConnectionPhaseReady, false, false, false},
		{"parked and observed", airflowConnectionPhaseParked, true, true, false},
		{"parked but unobserved", airflowConnectionPhaseParked, false, false, false},
		{"collision and observed", airflowConnectionPhaseCollision, true, false, true},
		// A collision reported about the old declaration is not this
		// declaration's answer either: aborting on it would report a state the
		// new spec may not even reach.
		{"collision but unobserved", airflowConnectionPhaseCollision, false, false, false},
		{"failed and observed", airflowConnectionPhaseFailed, true, false, true},
		{"failed but unobserved", airflowConnectionPhaseFailed, false, false, false},
		{"applying and observed", "Applying", true, false, false},
		// What a create's first poll sees: an object with no status at all.
		{"pending on create", "Pending", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applied := true
			settled, err := airflowConnectionSettled(&console.AirflowConnectionResponse{
				ConnId:       "warehouse",
				Phase:        tc.phase,
				SpecObserved: tc.observed,
				// Deliberately true in every case: a stale projection's
				// source_applied is true of the credential minted for the
				// PREVIOUS spec, so it cannot be what settles a wait.
				SourceApplied: &applied,
			})
			if settled != tc.wantSettled {
				t.Errorf("settled = %v, want %v", settled, tc.wantSettled)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want error = %v", err, tc.wantErr)
			}
		})
	}

	// An observed collision aborts, and the abort has to carry what the user
	// needs: the id, what holds it, and that the takeover is an explicit action
	// taken elsewhere.
	existing := "postgres"
	_, err := airflowConnectionSettled(&console.AirflowConnectionResponse{
		ConnId:                          "warehouse",
		Phase:                           airflowConnectionPhaseCollision,
		SpecObserved:                    true,
		CollisionExistingConnectionType: &existing,
	})
	if err == nil {
		t.Fatal("an observed Collision must abort the wait")
	}
	for _, want := range []string{"warehouse", "postgres", "takeover"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// ── conn_id, validated at plan time ───────────────────────────────────────

// TestAirflowConnectionConnIDValidators mirrors the API's own rules
// (`valid_conn_id` and `is_reserved_connection_id`): a conn_id it refuses has
// to fail the plan, not the apply — by which point the environment, database
// and bucket the connection depends on already exist, and `conn_id` forces
// replacement, so the mistake cannot be corrected in place.
func TestAirflowConnectionConnIDValidators(t *testing.T) {
	ctx := t.Context()
	s := resourceSchema(t, NewAirflowConnectionResource()).Schema
	attr, ok := s.Attributes["conn_id"].(fwschema.StringAttribute)
	if !ok {
		t.Fatalf("conn_id is %T, want a schema.StringAttribute whose validators can be read", s.Attributes["conn_id"])
	}
	if len(attr.Validators) == 0 {
		t.Fatal("conn_id declares no validators; every rule would be discovered as a 400 mid-apply")
	}

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"plain", "warehouse", false},
		{"dots and underscores", "dbaas.reporting_2", false},
		{"hyphen and mixed case", "a-b.c_D9", false},
		{"at the length limit", strings.Repeat("a", 200), false},

		{"a space", "my warehouse", true},
		{"a slash", "warehouse/1", true},
		{"non-ascii", "wärehouse", true},
		{"empty", "", true},
		{"one byte over the limit", strings.Repeat("a", 201), true},

		// The two ids the platform keeps for the rows it writes itself.
		{"reserved dag bucket", "dag_bucket_s3", true},
		{"reserved default", "hyperfluid_default", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validator.StringRequest{
				Path:           path.Root("conn_id"),
				PathExpression: path.MatchRoot("conn_id"),
				ConfigValue:    types.StringValue(tc.value),
			}
			resp := &validator.StringResponse{}
			for _, v := range attr.Validators {
				v.ValidateString(ctx, req, resp)
			}
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Errorf("error = %v, want %v (diagnostics: %v)", got, tc.wantErr, resp.Diagnostics)
			}
		})
	}

	// The reserved list is a copy of a platform constant, so keep the copy
	// honest about what it is copying.
	for _, want := range []string{"dag_bucket_s3", "hyperfluid_default"} {
		found := false
		for _, id := range airflowConnectionReservedIDs {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is reserved by the platform but missing from airflowConnectionReservedIDs", want)
		}
	}
}

// ── the compensating delete is confirmed ──────────────────────────────────

// TestAirflowConnectionRemoveConfirmsGone: a 204 from the delete route is an
// acceptance, not a release — the credential is revoked by the object's
// finalizer. Handing back a connection the platform allocated under an id
// nobody declared therefore has to poll until the object is gone, or the
// diagnostic tells the user a credential is revoked while a scoped database
// role or object-store identity is still live with nothing tracking it.
func TestAirflowConnectionRemoveConfirmsGone(t *testing.T) {
	ctx := t.Context()

	t.Run("gone", func(t *testing.T) {
		deletes, gets := 0, 0
		err := airflowConnectionRemove(ctx, time.Minute,
			func() error { deletes++; return nil },
			func() error { gets++; return client.ErrNotFound })
		if err != nil {
			t.Errorf("err = %v, want nil once the object is gone", err)
		}
		if deletes != 1 || gets != 1 {
			t.Errorf("deletes = %d, gets = %d, want one of each", deletes, gets)
		}
	})

	t.Run("delete refused", func(t *testing.T) {
		gets := 0
		err := airflowConnectionRemove(ctx, time.Minute,
			func() error { return errors.New("403 forbidden") },
			func() error { gets++; return nil })
		if err == nil {
			t.Fatal("err = nil, want the refusal")
		}
		if !strings.Contains(err.Error(), "403 forbidden") {
			t.Errorf("err = %v, want the API's own refusal", err)
		}
		if gets != 0 {
			t.Errorf("gets = %d, want none: there is nothing to confirm", gets)
		}
	})

	t.Run("still present", func(t *testing.T) {
		// The finalizer hang: the delete is accepted and the object stays.
		// The timeout is a nanosecond so the poll gives up on its first pass.
		err := airflowConnectionRemove(ctx, time.Nanosecond,
			func() error { return nil },
			func() error { return nil })
		if err == nil {
			t.Fatal("err = nil for a connection that is still there; the caller would report the credential as revoked")
		}
		if !strings.Contains(err.Error(), "still present") {
			t.Errorf("err = %v, want it to say the connection is still present", err)
		}
	})
}

// ── id is a usable import id ───────────────────────────────────────────────

// TestAirflowConnectionIDIsAUsableImportID closes the loop the schema used to
// advertise wrong: `id` is where a user reaches for an import id, so the string
// the mapper writes has to be the string ImportState parses.
func TestAirflowConnectionIDIsAUsableImportID(t *testing.T) {
	ctx := t.Context()
	airflowID := "11111111-1111-1111-1111-111111111111"
	model, d := airflowConnectionToModel(ctx, airflowID, &console.AirflowConnectionResponse{
		Name:                    "afconn-3f2a",
		ConnId:                  "warehouse",
		ConnectionType:          "postgres",
		ManagedPostgresqlRef:    ptr("warehouse"),
		ResolvedPermissionLevel: "viewer",
		Phase:                   airflowConnectionPhaseReady,
		SpecObserved:            true,
	})
	if d.HasError() {
		t.Fatalf("unexpected diagnostics: %v", d.Errors())
	}

	res := NewAirflowConnectionResource()
	importer, ok := res.(fwresource.ResourceWithImportState)
	if !ok {
		t.Fatal("the connection resource cannot be imported at all")
	}
	s := resourceSchema(t, res).Schema
	resp := &fwresource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)},
	}
	importer.ImportState(ctx, fwresource.ImportStateRequest{ID: model.ID.ValueString()}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("importing the id the resource itself exported failed: %v", resp.Diagnostics.Errors())
	}

	var imported types.String
	if d := resp.State.GetAttribute(ctx, path.Root("conn_id"), &imported); d.HasError() {
		t.Fatalf("reading conn_id back: %v", d.Errors())
	}
	if got := imported.ValueString(); got != "warehouse" {
		t.Errorf("imported conn_id = %q, want the conn_id the id carries", got)
	}
	var env types.String
	if d := resp.State.GetAttribute(ctx, path.Root("airflow"), &env); d.HasError() {
		t.Fatalf("reading airflow back: %v", d.Errors())
	}
	if got := env.ValueString(); got != airflowID {
		t.Errorf("imported airflow = %q, want %q", got, airflowID)
	}
}
