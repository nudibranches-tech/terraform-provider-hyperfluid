// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// hyperfluid_bucket_credentials data source — the derived S3 credentials for an
// existing bucket, minted by the console from the environment's object-storage
// owner. Use it to wire a bucket's access key / secret key / endpoint into an
// app's environment without pasting them by hand.
//
// The secret key lands in Terraform state, so treat state as sensitive (use a
// remote backend with encryption at rest). This is inherent to any credentials
// data source — state is the delivery mechanism.

var (
	_ datasource.DataSource              = &bucketCredentialsDataSource{}
	_ datasource.DataSourceWithConfigure = &bucketCredentialsDataSource{}
)

func NewBucketCredentialsDataSource() datasource.DataSource {
	return &bucketCredentialsDataSource{}
}

type bucketCredentialsDataSource struct {
	p *providerData
}

type bucketCredentialsDataSourceModel struct {
	Env        types.String `tfsdk:"env"`
	BucketName types.String `tfsdk:"bucket_name"`
	ID         types.String `tfsdk:"id"`
	AccessKey  types.String `tfsdk:"access_key"`
	SecretKey  types.String `tfsdk:"secret_key"`
	Endpoint   types.String `tfsdk:"endpoint"`
}

func (d *bucketCredentialsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_credentials"
}

func (d *bucketCredentialsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Derived S3 credentials for an existing bucket. **The `secret_key` is written to Terraform state — treat state as sensitive** (use an encrypted remote backend).",
		Attributes: map[string]schema.Attribute{
			"env":         schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the bucket lives in."},
			"bucket_name": schema.StringAttribute{Required: true, MarkdownDescription: "Bucket name to mint credentials for."},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Composite identifier `env/bucket_name`."},
			"access_key":  schema.StringAttribute{Computed: true, MarkdownDescription: "S3 access key id."},
			"secret_key":  schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "S3 secret access key."},
			"endpoint":    schema.StringAttribute{Computed: true, MarkdownDescription: "S3 endpoint the credentials authenticate against."},
		},
	}
}

func (d *bucketCredentialsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *bucketCredentialsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg bucketCredentialsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.BucketName.ValueString()
	creds, err := d.p.API.GetBucketCredentials(ctx, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Bucket not found", "no bucket named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to fetch bucket credentials", err.Error())
		return
	}

	cfg.ID = types.StringValue(env + "/" + name)
	cfg.AccessKey = types.StringValue(creds.AccessKey)
	cfg.SecretKey = types.StringValue(creds.SecretKey)
	cfg.Endpoint = types.StringValue(creds.Endpoint)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
