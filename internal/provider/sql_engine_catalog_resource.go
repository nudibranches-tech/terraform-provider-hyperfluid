// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

var (
	_ resource.Resource                   = &sqlEngineCatalogResource{}
	_ resource.ResourceWithConfigure      = &sqlEngineCatalogResource{}
	_ resource.ResourceWithImportState    = &sqlEngineCatalogResource{}
	_ resource.ResourceWithValidateConfig = &sqlEngineCatalogResource{}
)

const sqlEngineCatalogWaitTimeout = 5 * time.Minute

// catalogKinds maps the Terraform kind onto the API enum.
var catalogKinds = map[string]console.DataDockCatalogKind{
	"iceberg":    console.DataDockCatalogKindIceberg,
	"postgresql": console.DataDockCatalogKindPostgresql,
	"mysql":      console.DataDockCatalogKindMysql,
	"mariadb":    console.DataDockCatalogKindMariadb,
	"clickhouse": console.DataDockCatalogKindClickhouse,
	"tpch":       console.DataDockCatalogKindTpch,
	"tpcds":      console.DataDockCatalogKindTpcds,
	"faker":      console.DataDockCatalogKindFaker,
}

// catalogName is a Kubernetes DNS label, the name the catalog's resource takes.
var catalogName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func NewSqlEngineCatalogResource() resource.Resource {
	return &sqlEngineCatalogResource{}
}

type sqlEngineCatalogResource struct {
	p *providerData
}

type sqlEngineCatalogModel struct {
	ID                      types.String `tfsdk:"id"`
	ClusterID               types.String `tfsdk:"cluster_id"`
	Name                    types.String `tfsdk:"name"`
	Kind                    types.String `tfsdk:"kind"`
	BucketName              types.String `tfsdk:"bucket_name"`
	ManagedPostgresqlUserID types.String `tfsdk:"managed_postgresql_user_id"`
	ConnectionSecret        types.String `tfsdk:"connection_secret"`
	Phase                   types.String `tfsdk:"phase"`
	Message                 types.String `tfsdk:"message"`
}

func (r *sqlEngineCatalogResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sql_engine_catalog"
}

func (r *sqlEngineCatalogResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	source := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, MarkdownDescription: desc + " Not recovered by import. Changing this forces a new catalog.",
			PlanModifiers: []planmodifier.String{requiresReplaceIfKnown()},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A catalog of a SQL engine cluster: an Iceberg warehouse, an external database or a demo dataset. " +
			"Catalogs cannot be changed in place.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`<cluster_id>/<name>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "Id of the SQL engine cluster. Changing this forces a new catalog.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Catalog name: lowercase letters, digits and hyphens, at most 63. Changing this forces a new catalog.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(catalogName, "must be a DNS label of at most 63 characters")},
			},
			"kind": schema.StringAttribute{
				Required: true, MarkdownDescription: "Connector: `iceberg`, `postgresql`, `mysql`, `mariadb`, `clickhouse`, `tpch`, `tpcds` or `faker`. Changing this forces a new catalog.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.OneOf("iceberg", "postgresql", "mysql", "mariadb", "clickhouse", "tpch", "tpcds", "faker")},
			},
			"bucket_name":                source("Bucket backing the warehouse; `iceberg` only, and required there."),
			"managed_postgresql_user_id": source("Id of a `viewer` managed PostgreSQL user whose credentials the catalog uses; `postgresql` only."),
			"connection_secret":          source("Organization secret holding the connection bundle; required for `mysql`, `mariadb` and `clickhouse`, or for an external `postgresql`."),
			"phase":                      schema.StringAttribute{Computed: true, MarkdownDescription: "Reconcile phase, such as `Ready`."},
			"message":                    schema.StringAttribute{Computed: true, MarkdownDescription: "Last message from the platform."},
		},
	}
}

func (r *sqlEngineCatalogResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig holds each kind to the credential source the API accepts for
// it, so a mismatch fails the plan instead of the apply.
func (r *sqlEngineCatalogResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg sqlEngineCatalogModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Kind.IsUnknown() || cfg.Kind.IsNull() {
		return
	}
	validateCatalogSources(cfg, &resp.Diagnostics)
}

func validateCatalogSources(cfg sqlEngineCatalogModel, diags *diag.Diagnostics) {
	kind := cfg.Kind.ValueString()
	set := func(v types.String) bool { return !v.IsNull() }
	forbid := func(attr string, v types.String) {
		if set(v) {
			diags.AddAttributeError(path.Root(attr), "Attribute not allowed",
				fmt.Sprintf("`%s` does not apply to a `%s` catalog.", attr, kind))
		}
	}
	require := func(attr string, v types.String) {
		if v.IsNull() {
			diags.AddAttributeError(path.Root(attr), "Missing attribute",
				fmt.Sprintf("A `%s` catalog needs `%s`.", kind, attr))
		}
	}
	switch kind {
	case "iceberg":
		require("bucket_name", cfg.BucketName)
		forbid("managed_postgresql_user_id", cfg.ManagedPostgresqlUserID)
		forbid("connection_secret", cfg.ConnectionSecret)
	case "postgresql":
		forbid("bucket_name", cfg.BucketName)
		if cfg.ManagedPostgresqlUserID.IsUnknown() || cfg.ConnectionSecret.IsUnknown() {
			return
		}
		if set(cfg.ManagedPostgresqlUserID) == set(cfg.ConnectionSecret) {
			diags.AddAttributeError(path.Root("connection_secret"), "Exactly one credential source",
				"A `postgresql` catalog takes exactly one of `managed_postgresql_user_id` (a managed database) and `connection_secret` (an external one).")
		}
	case "mysql", "mariadb", "clickhouse":
		forbid("bucket_name", cfg.BucketName)
		forbid("managed_postgresql_user_id", cfg.ManagedPostgresqlUserID)
		require("connection_secret", cfg.ConnectionSecret)
	default:
		forbid("bucket_name", cfg.BucketName)
		forbid("managed_postgresql_user_id", cfg.ManagedPostgresqlUserID)
		forbid("connection_secret", cfg.ConnectionSecret)
	}
}

func (r *sqlEngineCatalogResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sqlEngineCatalogModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := console.CreateDataDockCatalogRequest{
		Name:                 plan.Name.ValueString(),
		Kind:                 catalogKinds[plan.Kind.ValueString()],
		BucketName:           stringPtr(plan.BucketName),
		ConnectionSecretName: stringPtr(plan.ConnectionSecret),
	}
	if !plan.ManagedPostgresqlUserID.IsNull() {
		u, err := uuid.Parse(plan.ManagedPostgresqlUserID.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("managed_postgresql_user_id"), "Invalid managed_postgresql_user_id", "must be a UUID: "+err.Error())
			return
		}
		body.ManagedPostgresqlUserId = &u
	}
	cluster, name := plan.ClusterID.ValueString(), plan.Name.ValueString()
	if err := r.p.API.CreateDataDockCatalog(ctx, cluster, body); err != nil {
		resp.Diagnostics.AddError("Failed to create SQL engine catalog", err.Error())
		return
	}
	plan.ID = types.StringValue(cluster + "/" + name)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)

	info, err := waitForReady(ctx, sqlEngineCatalogWaitTimeout, func() (*console.DataDockCatalogInfo, bool, error) {
		c, err := r.p.API.GetDataDockCatalog(ctx, cluster, name)
		if err != nil {
			return nil, false, err
		}
		if c.Phase != nil && *c.Phase == "Failed" {
			msg := "the catalog entered the Failed phase"
			if c.Message != nil && *c.Message != "" {
				msg = *c.Message
			}
			return nil, false, errors.New(msg)
		}
		return c, c.Phase != nil && *c.Phase == "Ready", nil
	})
	if err != nil {
		resp.Diagnostics.AddError("SQL engine catalog did not become ready", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, catalogState(plan, info))...)
}

func (r *sqlEngineCatalogResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior sqlEngineCatalogModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	info, err := r.p.API.GetDataDockCatalog(ctx, prior.ClusterID.ValueString(), prior.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SQL engine catalog", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, catalogState(prior, info))...)
}

// Update only records a credential source no read reports, adopted after an
// import; every other change replaces the catalog.
func (r *sqlEngineCatalogResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan sqlEngineCatalogModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	info, err := r.p.API.GetDataDockCatalog(ctx, plan.ClusterID.ValueString(), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SQL engine catalog", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, catalogState(plan, info))...)
}

func (r *sqlEngineCatalogResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sqlEngineCatalogModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cluster, name := state.ClusterID.ValueString(), state.Name.ValueString()
	if err := r.p.API.DeleteDataDockCatalog(ctx, cluster, name); err != nil {
		resp.Diagnostics.AddError("Failed to delete SQL engine catalog", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, sqlEngineCatalogWaitTimeout, func() error {
		_, err := r.p.API.GetDataDockCatalog(ctx, cluster, name)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("SQL engine catalog still present after delete", err.Error())
	}
}

func (r *sqlEngineCatalogResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cluster, name, err := splitID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// catalogState takes the identity and credential source from m, which no
// read reports, and the rest from the catalog.
func catalogState(m sqlEngineCatalogModel, info *console.DataDockCatalogInfo) sqlEngineCatalogModel {
	m.ID = types.StringValue(m.ClusterID.ValueString() + "/" + m.Name.ValueString())
	m.Kind = types.StringValue(info.Kind)
	m.Phase = optString(info.Phase)
	m.Message = optString(info.Message)
	return m
}
