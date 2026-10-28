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

var (
	_ datasource.DataSource              = &nextflowDataSource{}
	_ datasource.DataSourceWithConfigure = &nextflowDataSource{}
)

func NewNextflowDataSource() datasource.DataSource {
	return &nextflowDataSource{}
}

type nextflowDataSource struct {
	p *providerData
}

type nextflowDataSourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Env                  types.String `tfsdk:"env"`
	Name                 types.String `tfsdk:"name"`
	Description          types.String `tfsdk:"description"`
	Tags                 types.List   `tfsdk:"tags"`
	Buckets              types.Set    `tfsdk:"buckets"`
	Slug                 types.String `tfsdk:"slug"`
	Phase                types.String `tfsdk:"phase"`
	TaskNamespace        types.String `tfsdk:"task_namespace"`
	ResolvedRuntimeImage types.String `tfsdk:"resolved_runtime_image"`
	WorkVolumeCapacity   types.String `tfsdk:"work_volume_capacity"`
}

func (d *nextflowDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nextflow"
}

func (d *nextflowDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an environment's Nextflow deployment by name.",
		Attributes: map[string]schema.Attribute{
			"env":                    schema.StringAttribute{Required: true, MarkdownDescription: "Environment id."},
			"name":                   schema.StringAttribute{Required: true, MarkdownDescription: "Deployment name."},
			"id":                     cs("Deployment id."),
			"description":            cs("Free-form description."),
			"tags":                   schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"buckets":                schema.SetAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Buckets the pipelines may read and write."},
			"slug":                   cs("Derived slug."),
			"phase":                  cs("Lifecycle phase."),
			"task_namespace":         cs("Namespace the tasks run in."),
			"resolved_runtime_image": cs("Runtime image in use."),
			"work_volume_capacity":   cs("Bound capacity of the work volume."),
		},
	}
}

func (d *nextflowDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *nextflowDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg nextflowDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, name := cfg.Env.ValueString(), cfg.Name.ValueString()
	id, err := d.p.API.FindNextflow(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Nextflow not found", "no Nextflow deployment named "+name+" in environment "+env)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to look up Nextflow", featureNextflow, err)
		return
	}
	m, err := (&nextflowResource{p: d.p}).readInto(ctx, id)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read Nextflow", featureNextflow, err)
		return
	}
	out := nextflowDataSourceModel{
		ID: m.ID, Env: m.Env, Name: m.Name, Description: m.Description, Tags: m.Tags, Buckets: m.Buckets,
		Slug: m.Slug, Phase: m.Phase, TaskNamespace: m.TaskNamespace,
		ResolvedRuntimeImage: m.ResolvedRuntimeImage, WorkVolumeCapacity: m.WorkVolumeCapacity,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
