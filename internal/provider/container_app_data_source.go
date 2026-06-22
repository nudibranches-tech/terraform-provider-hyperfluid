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

// hyperfluid_container_app data source — look up an existing container app by
// name within an environment. Reuses the resource's containerAppModel + readInto
// so spec/status mapping lives in one place.

var (
	_ datasource.DataSource              = &containerAppDataSource{}
	_ datasource.DataSourceWithConfigure = &containerAppDataSource{}
)

func NewContainerAppDataSource() datasource.DataSource {
	return &containerAppDataSource{}
}

type containerAppDataSource struct {
	p *providerData
}

func (d *containerAppDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_app"
}

func (d *containerAppDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	ci := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing container app by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":                schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the app runs in."},
			"name":               schema.StringAttribute{Required: true, MarkdownDescription: "App name."},
			"id":                 cs("App id."),
			"image_repository":   cs("Container image repository."),
			"image_tag":          cs("Container image tag."),
			"port":               ci("Container port."),
			"replicas":           ci("Desired replica count."),
			"enabled":            schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the app is running."},
			"resource_tier":      cs("Resource tier — not returned by the API, so always null on a data source."),
			"health_check_path":  cs("HTTP health check path."),
			"health_check_port":  ci("HTTP health check port."),
			"resource_version":   cs("Kubernetes resourceVersion."),
			"cpu_request":        cs("CPU request derived from resource_tier."),
			"cpu_limit":          cs("CPU limit derived from resource_tier."),
			"memory_request":     cs("Memory request derived from resource_tier."),
			"memory_limit":       cs("Memory limit derived from resource_tier."),
			"phase":              cs("Current lifecycle phase."),
			"endpoint":           cs("Public endpoint, once provisioned."),
			"desired_replicas":   ci("Desired replicas reported by the platform."),
			"available_replicas": ci("Available replicas reported by the platform."),
		},
	}
}

func (d *containerAppDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *containerAppDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg containerAppModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.Name.ValueString()
	appID, err := d.p.API.FindContainerAppID(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Container app not found", "no container app named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up container app", err.Error())
		return
	}

	state, err := (&containerAppResource{p: d.p}).readInto(ctx, env, appID, types.StringNull())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read container app", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
