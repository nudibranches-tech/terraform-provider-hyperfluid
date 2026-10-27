// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// hyperfluid_service_account data source — look up an existing service account
// by client_id or by display name. Never returns its secret.

var (
	_ datasource.DataSource                     = &serviceAccountDataSource{}
	_ datasource.DataSourceWithConfigure        = &serviceAccountDataSource{}
	_ datasource.DataSourceWithConfigValidators = &serviceAccountDataSource{}
)

func NewServiceAccountDataSource() datasource.DataSource {
	return &serviceAccountDataSource{}
}

type serviceAccountDataSource struct {
	p *providerData
}

type serviceAccountDataSourceModel struct {
	ClientID          types.String `tfsdk:"client_id"`
	Name              types.String `tfsdk:"name"`
	ID                types.String `tfsdk:"id"`
	Description       types.String `tfsdk:"description"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	PublicClient      types.Bool   `tfsdk:"public_client"`
	PlatformManaged   types.Bool   `tfsdk:"platform_managed"`
	ManagedByResource types.String `tfsdk:"managed_by_resource_type"`
}

func (d *serviceAccountDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (d *serviceAccountDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing service account by `client_id` or by display `name`, to reference it " +
			"from roles, grants, groups, attributes and S3 keys. Service accounts are created in the console or with " +
			"hfctl, never by Terraform. The secret is " +
			"never returned. Requires `service_account:read`. The API lists at most the 100 most recently updated " +
			"accounts, so an older one may only be found by a `name` narrow enough to match it.",
		Attributes: map[string]schema.Attribute{
			"client_id": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "OAuth client id. Exactly one of `client_id` and `name` is required.",
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Display name; must match exactly one account.",
			},
			"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "Service account id (uuid)."},
			"description":   schema.StringAttribute{Computed: true, MarkdownDescription: "Description."},
			"enabled":       schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the account can obtain new tokens."},
			"public_client": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether it is a browser-flow client without a secret."},
			"platform_managed": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the platform owns the account (a backup target's, a registry's…); such an account cannot be changed.",
			},
			"managed_by_resource_type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Type of the resource owning a platform-managed account, e.g. `backup_target`.",
			},
		},
	}
}

func (d *serviceAccountDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("client_id"), path.MatchRoot("name")),
	}
}

func (d *serviceAccountDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	d.p = pd
}

func (d *serviceAccountDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg serviceAccountDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var sa *console.ServiceAccount
	var err error
	if !cfg.ClientID.IsNull() {
		sa, err = d.p.API.FindServiceAccount(ctx, d.p.OrgID, cfg.ClientID.ValueString())
	} else {
		sa, err = d.p.API.FindServiceAccountByName(ctx, d.p.OrgID, cfg.Name.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up service account", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceAccountDataSourceModel{
		ClientID:          types.StringValue(sa.IamSaClientId),
		Name:              types.StringValue(sa.Name),
		ID:                types.StringValue(sa.Id.String()),
		Description:       optString(sa.Description),
		Enabled:           types.BoolValue(sa.Enabled),
		PublicClient:      types.BoolValue(sa.PublicClient),
		PlatformManaged:   types.BoolValue(sa.ManagedByResourceType != nil),
		ManagedByResource: optString(sa.ManagedByResourceType),
	})...)
}
