// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// hyperfluid_model_serving data source — look up an existing model serving by its
// (server-assigned) name. Reuses the resource's model + readInto.

var (
	_ datasource.DataSource              = &modelServingDataSource{}
	_ datasource.DataSourceWithConfigure = &modelServingDataSource{}
)

func NewModelServingDataSource() datasource.DataSource {
	return &modelServingDataSource{}
}

type modelServingDataSource struct {
	p *providerData
}

func (d *modelServingDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model_serving"
}

func (d *modelServingDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	ci := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing model serving by name.",
		Attributes: map[string]schema.Attribute{
			"name":           schema.StringAttribute{Required: true, MarkdownDescription: "Model serving name."},
			"id":             cs("Resource identifier (same as name)."),
			"display_name":   cs("Human-readable display name."),
			"model_id":       cs("Model identifier."),
			"model_type":     cs("Model type (generation or embedding)."),
			"runtime":        cs("Serving runtime (vllm or tgi)."),
			"gpu":            ci("GPU count."),
			"memory":         cs("Memory request."),
			"max_model_len":  ci("Maximum model context length."),
			"replicas":       ci("Desired replica count."),
			"cpu":            cs("CPU request resolved by the platform."),
			"endpoint":       cs("OpenAI-compatible endpoint."),
			"phase":          cs("Current lifecycle phase."),
			"ready_replicas": ci("Ready replica count."),
		},
	}
}

func (d *modelServingDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *modelServingDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg modelServingModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()
	state, err := (&modelServingResource{p: d.p}).readInto(ctx, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Model serving not found", "no model serving named "+name)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read model serving", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
