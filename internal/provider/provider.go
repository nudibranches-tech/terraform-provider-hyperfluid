// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// Ensure HyperfluidProvider satisfies the provider interface.
var _ provider.Provider = &HyperfluidProvider{}

// HyperfluidProvider defines the provider implementation.
type HyperfluidProvider struct {
	// version is the provider version on release, "dev" locally, "test" in
	// acceptance testing.
	version string
}

// providerData is injected into every resource/data source via Configure().
type providerData struct {
	API   *client.Client
	OrgID string
}

// HyperfluidProviderModel describes the provider configuration block.
type HyperfluidProviderModel struct {
	Endpoint        types.String `tfsdk:"endpoint"`
	OrganizationID  types.String `tfsdk:"organization_id"`
	CredentialsFile types.String `tfsdk:"credentials_file"`
}

func (p *HyperfluidProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hyperfluid" // → resource prefix `hyperfluid_*`
	resp.Version = p.version
}

func (p *HyperfluidProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage Hyperfluid platform resources through the Console external API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Console API base URL. Falls back to `HYPERFLUID_ENDPOINT`, then `console_url` in the credentials file.",
			},
			"organization_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Organization ID. Falls back to `HYPERFLUID_ORGANIZATION_ID`, then the credentials file.",
			},
			"credentials_file": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Path to the service-account JSON (the same file hfctl consumes). Falls back to `HYPERFLUID_CREDENTIALS`. The secret is never written to state.",
			},
		},
	}
}

func (p *HyperfluidProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg HyperfluidProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := firstNonEmpty(cfg.Endpoint.ValueString(), os.Getenv("HYPERFLUID_ENDPOINT"))
	credsFile := firstNonEmpty(cfg.CredentialsFile.ValueString(), os.Getenv("HYPERFLUID_CREDENTIALS"))
	if credsFile == "" {
		resp.Diagnostics.AddError(
			"Missing credentials",
			"Set the provider `credentials_file` or the HYPERFLUID_CREDENTIALS environment variable to the service-account JSON path.",
		)
		return
	}

	api, orgID, err := client.NewFromServiceAccount(endpoint, credsFile)
	if err != nil {
		resp.Diagnostics.AddError("Authentication failed", err.Error())
		return
	}
	if v := firstNonEmpty(cfg.OrganizationID.ValueString(), os.Getenv("HYPERFLUID_ORGANIZATION_ID")); v != "" {
		orgID = v
	}
	if orgID == "" {
		resp.Diagnostics.AddError(
			"Missing organization",
			"organization_id was not found in the provider config, HYPERFLUID_ORGANIZATION_ID, or the credentials file.",
		)
		return
	}

	pd := &providerData{API: api, OrgID: orgID}
	resp.ResourceData = pd
	resp.DataSourceData = pd
}

func (p *HyperfluidProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewBucketResource,
		NewContainerAppResource,
		NewManagedPostgresqlResource,
		NewManagedPostgresqlUserResource,
		// M1 remaining: backup_target, app_instance, secret.
	}
}

func (p *HyperfluidProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewHarborDataSource,
		// M1+: storage, bifrost, app_template, bucket_credentials, registry views ...
	}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &HyperfluidProvider{version: version}
	}
}
