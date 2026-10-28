// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// hyperfluid_user data source — look up an organization member by email.

var (
	_ datasource.DataSource              = &userDataSource{}
	_ datasource.DataSourceWithConfigure = &userDataSource{}
)

func NewUserDataSource() datasource.DataSource {
	return &userDataSource{}
}

type userDataSource struct {
	p *providerData
}

type userDataSourceModel struct {
	Email     types.String    `tfsdk:"email"`
	ID        types.String    `tfsdk:"id"`
	FirstName types.String    `tfsdk:"first_name"`
	LastName  types.String    `tfsdk:"last_name"`
	Roles     []userRoleModel `tfsdk:"roles"`
}

type userRoleModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a member of the organization by email, e.g. to get the `user_id` of " +
			"`hyperfluid_user_access`. Needs the `user:read` permission.",
		Attributes: map[string]schema.Attribute{
			"email":      schema.StringAttribute{Required: true, MarkdownDescription: "Email of the member, matched case-insensitively."},
			"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "User id."},
			"first_name": schema.StringAttribute{Computed: true, MarkdownDescription: "First name."},
			"last_name":  schema.StringAttribute{Computed: true, MarkdownDescription: "Last name."},
			"roles": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Organization-wide roles of the user.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Role id."},
					"name": schema.StringAttribute{Computed: true, MarkdownDescription: "Role name."},
				}},
			},
		},
	}
}

func (d *userDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg userDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	email := strings.TrimSpace(cfg.Email.ValueString())
	u, err := d.p.API.FindOrgUserByEmail(ctx, d.p.OrgID, email)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("User not found", fmt.Sprintf(
			"no member of the organization has the email %q; the user must be a member of the organization "+
				"(invited, and the invitation accepted) before Terraform can reference them", email))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up user", err.Error())
		return
	}
	roles := make([]userRoleModel, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, userRoleModel{ID: types.StringValue(r.Id.String()), Name: types.StringValue(r.Name)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userDataSourceModel{
		Email:     cfg.Email,
		ID:        types.StringValue(u.Id.String()),
		FirstName: types.StringValue(u.FirstName),
		LastName:  types.StringValue(u.LastName),
		Roles:     roles,
	})...)
}
