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

// hyperfluid_shared_model data source — look up a platform shared model (the
// catalog of models the org can consume) by name. Read-only.

var (
	_ datasource.DataSource              = &sharedModelDataSource{}
	_ datasource.DataSourceWithConfigure = &sharedModelDataSource{}
)

func NewSharedModelDataSource() datasource.DataSource {
	return &sharedModelDataSource{}
}

type sharedModelDataSource struct {
	p *providerData
}

type sharedModelModel struct {
	Name            types.String `tfsdk:"name"`
	ModelType       types.String `tfsdk:"model_type"`
	Runtime         types.String `tfsdk:"runtime"`
	Cpu             types.String `tfsdk:"cpu"`
	Gpu             types.Int64  `tfsdk:"gpu"`
	Memory          types.String `tfsdk:"memory"`
	Phase           types.String `tfsdk:"phase"`
	Endpoint        types.String `tfsdk:"endpoint"`
	ServedModelName types.String `tfsdk:"served_model_name"`
}

func (d *sharedModelDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_shared_model"
}

func (d *sharedModelDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a platform shared model — a model the organization can consume — by name.",
		Attributes: map[string]schema.Attribute{
			"name":              schema.StringAttribute{Required: true, MarkdownDescription: "Shared model name."},
			"model_type":        cs("Model type (generation or embedding)."),
			"runtime":           cs("Serving runtime (vllm or tgi)."),
			"cpu":               cs("CPU request."),
			"gpu":               schema.Int64Attribute{Computed: true, MarkdownDescription: "GPU count."},
			"memory":            cs("Memory request."),
			"phase":             cs("Current lifecycle phase."),
			"endpoint":          cs("OpenAI-compatible endpoint."),
			"served_model_name": cs("Served model name override, if any."),
		},
	}
}

func (d *sharedModelDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *sharedModelDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg sharedModelModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()
	m, err := d.p.API.GetSharedModel(ctx, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Shared model not found", "no shared model named "+name)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read shared model", err.Error())
		return
	}

	state := sharedModelModel{
		Name:            types.StringValue(m.Name),
		ModelType:       types.StringValue(m.ModelType),
		Runtime:         types.StringValue(m.Runtime),
		Cpu:             types.StringValue(m.Cpu),
		Gpu:             types.Int64Value(int64(m.Gpu)),
		Memory:          types.StringValue(m.Memory),
		Phase:           types.StringValue(m.Phase),
		Endpoint:        optString(m.Endpoint),
		ServedModelName: optString(m.ServedModelName),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
