// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// airflow_connection_resource.go — a managed Airflow connection: the platform
// mints the credential for a database or a bucket and writes the Airflow
// connection row itself, so a DAG asks for `conn_id` and never holds a secret.
//
// Two names, and they are not interchangeable: `conn_id` is what the DAG asks
// for, `name` is the connection object's own name and the only one the API
// accepts in a URL. Everything here is keyed by `conn_id` and resolved through
// the client wrapper.

var (
	_ resource.Resource                     = &airflowConnectionResource{}
	_ resource.ResourceWithConfigure        = &airflowConnectionResource{}
	_ resource.ResourceWithImportState      = &airflowConnectionResource{}
	_ resource.ResourceWithConfigValidators = &airflowConnectionResource{}
)

// airflowConnectionWaitTimeout is short next to the environment's own: a
// connection provisions no infrastructure, it resolves an existing target,
// mints a scoped credential and writes one Airflow row.
const airflowConnectionWaitTimeout = 5 * time.Minute

// Phases the connection controller publishes.
const (
	airflowConnectionPhaseReady     = "Ready"
	airflowConnectionPhaseParked    = "Parked"
	airflowConnectionPhaseCollision = "Collision"
	airflowConnectionPhaseFailed    = "Failed"
)

// airflowPermissionLevels are the only two levels a connection can pin. What
// each means depends on the target: database grants for a Postgres connection,
// the S3 verbs of a scoped object-store identity for a bucket one.
var airflowPermissionLevels = []string{
	string(console.PermissionLevelViewer),
	string(console.PermissionLevelEditor),
}

func NewAirflowConnectionResource() resource.Resource {
	return &airflowConnectionResource{}
}

type airflowConnectionResource struct {
	p *providerData
}

type airflowConnectionModel struct {
	ID                   types.String `tfsdk:"id"`
	Airflow              types.String `tfsdk:"airflow"`
	ConnID               types.String `tfsdk:"conn_id"`
	ManagedPostgresqlRef types.String `tfsdk:"managed_postgresql_ref"`
	BucketRef            types.String `tfsdk:"bucket_ref"`
	PermissionLevel      types.String `tfsdk:"permission_level"`

	// computed
	Name                            types.String `tfsdk:"name"`
	ConnectionType                  types.String `tfsdk:"connection_type"`
	ResolvedPermissionLevel         types.String `tfsdk:"resolved_permission_level"`
	Phase                           types.String `tfsdk:"phase"`
	SpecObserved                    types.Bool   `tfsdk:"spec_observed"`
	SourceApplied                   types.Bool   `tfsdk:"source_applied"`
	Collision                       types.Bool   `tfsdk:"collision"`
	CollisionExistingConnectionType types.String `tfsdk:"collision_existing_connection_type"`
	Conditions                      types.List   `tfsdk:"conditions"`
}

type airflowConnectionConditionModel struct {
	Type    types.String `tfsdk:"type"`
	Status  types.String `tfsdk:"status"`
	Reason  types.String `tfsdk:"reason"`
	Message types.String `tfsdk:"message"`
}

func airflowConnectionConditionType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"type":    types.StringType,
		"status":  types.StringType,
		"reason":  types.StringType,
		"message": types.StringType,
	}}
}

func (r *airflowConnectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_airflow_connection"
}

// ConfigValidators enforces the one-target rule at plan time. The API answers
// 400 for both zero and two targets, but discovering that during apply — after
// the rest of the configuration has already been created — is the wrong place
// to learn it, and a check written inside Create could only ever fire there.
func (r *airflowConnectionResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("managed_postgresql_ref"),
			path.MatchRoot("bucket_ref"),
		),
	}
}

func (r *airflowConnectionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	computedBool := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed Airflow connection: the platform resolves the target, mints a " +
			"credential scoped to it and writes the Airflow connection row itself. A DAG then asks for " +
			"the `conn_id` and never holds a secret — nothing here puts a password in Terraform state, " +
			"because no password ever reaches Terraform.\n\n" +
			"A connection names exactly one target — `managed_postgresql_ref` **or** `bucket_ref` — and " +
			"which one it is decides the connection's type. Both or neither is a configuration error, " +
			"reported at plan time.\n\n" +
			"~> **Object-store credentials are not exposed here.** A bucket connection's credential is " +
			"minted fresh, short-lived and valid only against the in-cluster gateway that issued it, so " +
			"it has no sane representation in Terraform state: by the time state were written the " +
			"credential would already be expiring, and it would be useless anywhere but inside the " +
			"cluster. DAGs get it from the connection at run time; there is no Terraform attribute, and " +
			"no data source, that hands it out.\n\n" +
			"Import id is `\"<airflow_id>/<conn_id>\"`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite identifier `<airflow_id>/<name>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"airflow": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Id of the `hyperfluid_airflow` environment this connection belongs to. " +
					"Changing this forces a new connection.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"conn_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The connection id a DAG asks Airflow for, e.g. `warehouse`. It has to " +
					"be unique within the environment, and the two ways it can already be taken end " +
					"differently.\n\n" +
					"A `conn_id` another **managed** connection in the same environment already holds is " +
					"refused here, before anything is created. The platform's own create route answers a " +
					"taken id by allocating `warehouse-1`, `warehouse-2`, … instead of failing — sensible " +
					"for a console form, wrong for a declaration that asked for one specific id — so this " +
					"resource looks first, and checks afterwards that it got the id it asked for. Adopt an " +
					"existing connection with `terraform import` rather than declaring it a second " +
					"time.\n\n" +
					"A `conn_id` held by a **foreign** row — one written by hand in the Airflow UI — is " +
					"never overwritten: the connection reports phase `Collision` and is not applied, and " +
					"taking the id over is an explicit action from the console or `hfctl`.\n\n" +
					"Changing this forces a new connection.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 200)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"managed_postgresql_ref": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of a `hyperfluid_managed_postgresql` in the same harbor to connect " +
					"to. Exactly one of this and `bucket_ref` must be set. Switching a connection from one " +
					"target to the other is applied in place: the platform releases the credential it no " +
					"longer needs before minting the new one.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 63)},
			},
			"bucket_ref": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of a `hyperfluid_bucket` in the same harbor to connect to. Exactly " +
					"one of this and `managed_postgresql_ref` must be set.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 63)},
			},
			"permission_level": schema.StringAttribute{
				// Optional and deliberately NOT Computed, and never defaulted
				// here. Leaving it out is its own state: the platform resolves
				// its default on every reconcile, so an unpinned connection
				// follows a default that may later be narrowed, while a pinned
				// one keeps exactly what it asked for. Writing `viewer` into
				// the object on the user's behalf would erase that difference
				// permanently.
				Optional: true,
				MarkdownDescription: "Pin the access level granted on the target: `viewer` or `editor`. Leave " +
					"it out to follow the platform default, which is resolved on every reconcile and is " +
					"therefore a different thing from pinning today's default value — the level actually " +
					"in force is always reported as `resolved_permission_level`. What the level means " +
					"depends on the target: database grants for a PostgreSQL connection, the object-store " +
					"verbs of a scoped identity for a bucket one.\n\n" +
					"~> **Removing the pin forces a new connection.** The API can raise or lower a pinned " +
					"level in place but has no way to un-pin one, so going back to the platform default " +
					"means replacing the connection — which re-mints its credential.",
				Validators: []validator.String{stringvalidator.OneOf(airflowPermissionLevels...)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIf(
						func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
							resp.RequiresReplace = airflowConnectionUnpinRequiresReplace(req.StateValue, req.ConfigValue)
						},
						"Removing permission_level forces a new connection.",
						"Removing `permission_level` forces a new connection: the API cannot un-pin a level in place.",
					),
				},
			},

			"name": computedStr("The connection object's own name, which is how the API addresses it. " +
				"Derived by the platform and not the same string as `conn_id`."),
			"connection_type": computedStr("Type of connection, derived from which target was named: a " +
				"PostgreSQL connection or a bucket one."),
			"resolved_permission_level": computedStr("The level actually in force: the value pinned in " +
				"`permission_level`, or the platform's own default when nothing is pinned."),
			"phase": computedStr("Lifecycle phase: `Pending`, `WaitingForDependency`, `Collision`, " +
				"`TakingOver`, `Applying`, `Ready`, `Parked`, `Deleting` or `Failed`. `Parked` is not a " +
				"failure — it is what a connection reports while its environment is asleep."),
			"spec_observed": computedBool("Whether the platform has looked at the current declaration yet. " +
				"False distinguishes \"not seen\" from \"seen and cannot be satisfied\", which otherwise " +
				"look identical."),
			"source_applied": computedBool("Whether the credential has actually been applied to the target."),
			"collision": computedBool("Whether a connection row the platform does not manage — one written " +
				"by hand in the Airflow UI, or left behind by something other than Hyperfluid — already " +
				"owns this `conn_id`. A colliding connection is not applied. Resolving a collision " +
				"requires an explicit takeover, which is done from the console or `hfctl`, not from " +
				"Terraform. A `conn_id` a second Terraform declaration asks for is not this: that is " +
				"refused at create time, before any connection exists to collide."),
			"collision_existing_connection_type": computedStr("Type of the connection already holding this " +
				"`conn_id`, when there is a collision."),
			"conditions": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "The platform's status conditions for this connection — the detail " +
					"behind `phase`, and where the reason for a connection that will not apply is written.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type":    computedStr("Condition type."),
						"status":  computedStr("`True`, `False` or `Unknown`."),
						"reason":  computedStr("Machine-readable reason."),
						"message": computedStr("Human-readable explanation."),
					},
				},
			},
		},
	}
}

// airflowConnectionUnpinRequiresReplace reports whether dropping
// `permission_level` from the configuration has to be applied by replacing the
// connection. Un-pinning is not expressible through PATCH — the API reads the
// field only when it is present — so a removed attribute would otherwise leave
// yesterday's pin in force while state claimed the platform default was back.
// Raising or lowering a pin, and adding one, are all in-place changes.
func airflowConnectionUnpinRequiresReplace(state, config types.String) bool {
	return !state.IsNull() && config.IsNull()
}

func (r *airflowConnectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	r.p = pd
}

func (r *airflowConnectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan airflowConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	airflowID := plan.Airflow.ValueString()
	connID := plan.ConnID.ValueString()

	// POST is not "create this conn_id or fail". The platform derives the
	// connection object's name from (environment, conn_id), and when that name
	// is already taken it allocates `conn_id-1`, `conn_id-2`, … rather than
	// refusing — right for a console form, wrong for a declaration that named
	// one specific id. Look before writing, so a conn_id that is already
	// managed here is a plain "import it instead" and not a second,
	// credential-bearing connection under an id nobody declared.
	existing, err := r.p.API.FindAirflowConnection(ctx, r.p.OrgID, airflowID, connID)
	switch {
	case err == nil:
		summary, detail := airflowConnectionExistsDiagnostic(connID, airflowID, existing)
		resp.Diagnostics.AddError(summary, detail)
		return
	case errors.Is(err, client.ErrNotFound):
		// Free, as the plan assumed.
	default:
		resp.Diagnostics.AddError("Failed to check for an existing Airflow connection", err.Error())
		return
	}

	body := console.CreateAirflowConnectionRequest{
		ConnId:               connID,
		ManagedPostgresqlRef: stringPtr(plan.ManagedPostgresqlRef),
		BucketRef:            stringPtr(plan.BucketRef),
	}
	// Absent stays absent: a body that materialised the platform default would
	// pin it into the object and destroy the "unpinned" state for good.
	if level := stringPtr(plan.PermissionLevel); level != nil {
		body.PermissionLevel = ptr(console.PermissionLevel(*level))
	}

	created, err := r.p.API.CreateAirflowConnection(ctx, r.p.OrgID, airflowID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Airflow connection", err.Error())
		return
	}
	// The look-first above closes the ordinary case but cannot close the race:
	// a concurrent create can land between the list and the POST, and then the
	// platform allocates a suffixed id instead of answering 409. Storing that
	// would fail the apply on an inconsistent result, and because `conn_id`
	// forces replacement the next plan would destroy the suffixed connection
	// and re-create it into the same collision, for good. Hand the credential
	// back and stop instead.
	if created.ConnId != connID {
		removalErr := r.p.API.DeleteAirflowConnection(ctx, r.p.OrgID, airflowID, created.Name)
		summary, detail := airflowConnectionRenamedDiagnostic(connID, created.ConnId, created.Name, removalErr)
		resp.Diagnostics.AddError(summary, detail)
		return
	}

	settled, err := r.waitApplied(ctx, airflowID, created.Name)
	if err != nil {
		// The connection EXISTS from here on: the POST succeeded and only the
		// wait did not. Persisting what we know before returning the error is
		// what keeps it inside Terraform's world — a bare `return` here leaves
		// an object that state never learned about, so the next plan cannot
		// refresh it and `destroy` cannot remove it, and the only ways out are
		// an import or deleting it by hand. With the id stored, the next
		// `apply` reconciles it and `destroy` cleans it up.
		if partial, d := airflowConnectionToModel(ctx, airflowID, created); !d.HasError() {
			resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)
		}
		resp.Diagnostics.AddError(
			"Airflow connection did not become ready",
			err.Error()+"\n\nThe connection was created and is now tracked in state, so it is not "+
				"orphaned: fix the cause and re-apply, or run `terraform destroy` to remove it.")
		return
	}
	state, d := airflowConnectionToModel(ctx, airflowID, settled)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *airflowConnectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior airflowConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	airflowID := prior.Airflow.ValueString()
	// Resolved by conn_id rather than by the stored name, so an imported
	// connection — which knows only the conn_id — takes the same path.
	conn, err := r.p.API.FindAirflowConnection(ctx, r.p.OrgID, airflowID, prior.ConnID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Airflow connection", err.Error())
		return
	}
	state, d := airflowConnectionToModel(ctx, airflowID, conn)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *airflowConnectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state airflowConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var body console.PatchAirflowConnectionRequest
	// A retarget replaces the whole payload, so the new reference is sent on its
	// own and the abandoned one is simply absent — a body carrying both is the
	// one shape the platform refuses outright.
	if !plan.ManagedPostgresqlRef.Equal(state.ManagedPostgresqlRef) || !plan.BucketRef.Equal(state.BucketRef) {
		body.ManagedPostgresqlRef = stringPtr(plan.ManagedPostgresqlRef)
		body.BucketRef = stringPtr(plan.BucketRef)
	}
	// Only a present level is read by the API, which is why removing the pin
	// forces replacement (see the schema) and never reaches this path.
	if !plan.PermissionLevel.Equal(state.PermissionLevel) {
		if level := stringPtr(plan.PermissionLevel); level != nil {
			body.PermissionLevel = ptr(console.PermissionLevel(*level))
		}
	}

	airflowID := state.Airflow.ValueString()
	name, err := r.connectionName(ctx, airflowID, state)
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve the Airflow connection", err.Error())
		return
	}
	if err := r.p.API.PatchAirflowConnection(ctx, r.p.OrgID, airflowID, name, body); err != nil {
		resp.Diagnostics.AddError("Failed to update Airflow connection", err.Error())
		return
	}
	settled, err := r.waitApplied(ctx, airflowID, name)
	if err != nil {
		resp.Diagnostics.AddError("Airflow connection did not become ready after update", err.Error())
		return
	}
	newState, d := airflowConnectionToModel(ctx, airflowID, settled)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *airflowConnectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state airflowConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	airflowID := state.Airflow.ValueString()
	name, err := r.connectionName(ctx, airflowID, state)
	if errors.Is(err, client.ErrNotFound) {
		// Already gone; nothing to delete.
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve the Airflow connection", err.Error())
		return
	}
	if err := r.p.API.DeleteAirflowConnection(ctx, r.p.OrgID, airflowID, name); err != nil {
		resp.Diagnostics.AddError("Failed to delete Airflow connection", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, airflowConnectionWaitTimeout, func() error {
		_, err := r.p.API.GetAirflowConnection(ctx, r.p.OrgID, airflowID, name)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Airflow connection still present after delete", err.Error())
	}
}

// connectionName returns the object name the API addresses this connection by.
// It is in state after any read, but an import followed by an apply with
// refresh disabled can reach a write with only the conn_id known, and a write
// to an empty name is a request to a different URL altogether.
func (r *airflowConnectionResource) connectionName(ctx context.Context, airflowID string, state airflowConnectionModel) (string, error) {
	if name := state.Name.ValueString(); name != "" {
		return name, nil
	}
	conn, err := r.p.API.FindAirflowConnection(ctx, r.p.OrgID, airflowID, state.ConnID.ValueString())
	if err != nil {
		return "", err
	}
	return conn.Name, nil
}

// ImportState parses "<airflow_id>/<conn_id>" — the conn_id, not the object
// name, because the conn_id is the identifier a user knows. Read resolves it
// into the name the API addresses.
func (r *airflowConnectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	airflowID, connID, err := splitID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import id",
			"expected an import id in the form \"<airflow_id>/<conn_id>\", got "+fmt.Sprintf("%q", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("airflow"), airflowID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("conn_id"), connID)...)
}

// waitApplied blocks until the connection settles. `Parked` counts as settled:
// a connection on a sleeping environment is deliberately not applied, and
// waiting for it would be a guaranteed timeout. `Collision` and `Failed` abort
// at once — both are states a retry cannot leave, and both carry the reason the
// user needs.
func (r *airflowConnectionResource) waitApplied(ctx context.Context, airflowID, name string) (*console.AirflowConnectionResponse, error) {
	var last *console.AirflowConnectionResponse
	settled, err := waitForReady(ctx, airflowConnectionWaitTimeout, func() (*console.AirflowConnectionResponse, bool, error) {
		conn, err := r.p.API.GetAirflowConnection(ctx, r.p.OrgID, airflowID, name)
		if err != nil {
			return nil, false, err
		}
		last = conn
		switch conn.Phase {
		case airflowConnectionPhaseReady, airflowConnectionPhaseParked:
			return conn, true, nil
		case airflowConnectionPhaseCollision:
			return nil, false, fmt.Errorf(
				"the conn_id %q is already owned by an Airflow connection row the platform does not "+
					"manage%s, so this one was not applied; resolve the collision from the console or "+
					"with hfctl, which can authorize an explicit takeover",
				conn.ConnId, airflowConnectionCollisionSuffix(conn))
		case airflowConnectionPhaseFailed:
			return nil, false, fmt.Errorf("the connection reported phase Failed: %s", airflowConnectionDetail(conn))
		default:
			return conn, false, nil
		}
	})
	if err == nil {
		return settled, nil
	}
	if last != nil {
		return nil, fmt.Errorf("%w (last reported phase %q: %s)", err, last.Phase, airflowConnectionDetail(last))
	}
	return nil, err
}

// airflowConnectionExistsDiagnostic explains a conn_id that is already held by
// a managed connection in this environment. Creating a second declaration of it
// does not fail server-side — the platform would allocate a suffixed id — so
// the honest answer is that this connection already exists and wants adopting.
func airflowConnectionExistsDiagnostic(connID, airflowID string, existing *console.AirflowConnectionResponse) (summary, detail string) {
	kind := ""
	if existing != nil && existing.ConnectionType != "" {
		kind = " (a " + existing.ConnectionType + " connection, object " + existing.Name + ")"
	} else if existing != nil && existing.Name != "" {
		kind = " (object " + existing.Name + ")"
	}
	return "Airflow connection already exists",
		fmt.Sprintf("A managed connection with conn_id %q already exists in this Airflow environment%s.\n\n"+
			"Creating it again would not fail: the platform allocates the next free id — %q, %q and so "+
			"on — which would leave a second, credential-bearing connection under an id nothing "+
			"declared. Import the existing one instead:\n\n"+
			"    terraform import <this resource's address> %s/%s\n\n"+
			"or, if the two declarations are genuinely meant to be different connections, give this one "+
			"its own conn_id.",
			connID, kind, connID+"-1", connID+"-2", airflowID, connID)
}

// airflowConnectionRenamedDiagnostic reports a create that came back under a
// different conn_id than it asked for — the suffix the platform allocates when
// the id is taken. Reachable only by losing a race with a concurrent create,
// and never storable: the id is the resource's identity. `removalErr` is the
// result of giving the unwanted connection back, and it decides the middle
// paragraph: whether the user has anything left to clean up is the one thing
// this message must not get wrong.
func airflowConnectionRenamedDiagnostic(requested, allocated, name string, removalErr error) (summary, detail string) {
	outcome := fmt.Sprintf("%q is not the connection that was planned, so it has not been kept: the object %q it "+
		"was created as has been deleted, and the credential minted for it is revoked as that object "+
		"finalizes. Nothing was written to state.", allocated, name)
	if removalErr != nil {
		outcome = fmt.Sprintf("%q is not the connection that was planned, but removing it again failed (%s), so it "+
			"is still live as the object %q and still holds a credential on its target. Delete it from the "+
			"console, or with `hfctl airflow connections delete %s`. Nothing was written to state, so "+
			"Terraform will not clean it up for you.", allocated, removalErr, name, allocated)
	}
	return "The platform allocated a different conn_id",
		fmt.Sprintf("This connection asked for conn_id %q, but the platform answered with %q: the id was "+
			"taken between the check for it and the create, so the next free one was allocated "+
			"instead.\n\n%s\n\n"+
			"Find out what already holds %q — another declaration, the console, or `hfctl` — then either "+
			"adopt it with `terraform import` or declare this connection under a different conn_id.",
			requested, allocated, outcome, requested)
}

func airflowConnectionCollisionSuffix(conn *console.AirflowConnectionResponse) string {
	if conn.CollisionExistingConnectionType == nil || *conn.CollisionExistingConnectionType == "" {
		return ""
	}
	return " (a " + *conn.CollisionExistingConnectionType + " connection)"
}

// airflowConnectionDetail summarises why a connection is not applied: the first
// condition that is not satisfied, which is where the controller writes the
// reason. Falls back to a statement of what is known rather than to silence.
func airflowConnectionDetail(conn *console.AirflowConnectionResponse) string {
	for _, c := range conn.Conditions {
		if c.Status == "True" {
			continue
		}
		parts := make([]string, 0, 3)
		if c.Type != "" {
			parts = append(parts, c.Type)
		}
		if c.Reason != nil && *c.Reason != "" {
			parts = append(parts, *c.Reason)
		}
		if c.Message != nil && *c.Message != "" {
			parts = append(parts, *c.Message)
		}
		if len(parts) > 0 {
			return strings.Join(parts, ": ")
		}
	}
	if !conn.SpecObserved {
		return "the platform has not observed this declaration yet"
	}
	return "no unsatisfied condition was reported"
}

// airflowConnectionToModel maps the API view into state. `permission_level`
// comes straight from the projection, which reports the pin and only the pin —
// absent when the user pinned nothing — so an unpinned connection stays
// unpinned in state and a level someone pinned elsewhere shows up as drift.
func airflowConnectionToModel(ctx context.Context, airflowID string, conn *console.AirflowConnectionResponse) (airflowConnectionModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	models := make([]airflowConnectionConditionModel, 0, len(conn.Conditions))
	for _, c := range conn.Conditions {
		models = append(models, airflowConnectionConditionModel{
			Type:    types.StringValue(c.Type),
			Status:  types.StringValue(c.Status),
			Reason:  optString(c.Reason),
			Message: optString(c.Message),
		})
	}
	conditions, d := types.ListValueFrom(ctx, airflowConnectionConditionType(), models)
	diags.Append(d...)

	return airflowConnectionModel{
		ID:                              types.StringValue(airflowID + "/" + conn.Name),
		Airflow:                         types.StringValue(airflowID),
		ConnID:                          types.StringValue(conn.ConnId),
		ManagedPostgresqlRef:            optString(conn.ManagedPostgresqlRef),
		BucketRef:                       optString(conn.BucketRef),
		PermissionLevel:                 optString(conn.PermissionLevel),
		Name:                            types.StringValue(conn.Name),
		ConnectionType:                  types.StringValue(conn.ConnectionType),
		ResolvedPermissionLevel:         types.StringValue(conn.ResolvedPermissionLevel),
		Phase:                           types.StringValue(conn.Phase),
		SpecObserved:                    types.BoolValue(conn.SpecObserved),
		SourceApplied:                   types.BoolValue(conn.SourceApplied != nil && *conn.SourceApplied),
		Collision:                       types.BoolValue(conn.Collision),
		CollisionExistingConnectionType: optString(conn.CollisionExistingConnectionType),
		Conditions:                      conditions,
	}, diags
}
