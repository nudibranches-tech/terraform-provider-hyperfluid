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

// hyperfluid_service_link data source — look up a link by name within an
// environment, for a link declared through the console or hfctl. Reuses the
// resource's model and mapping.

var (
	_ datasource.DataSource              = &serviceLinkDataSource{}
	_ datasource.DataSourceWithConfigure = &serviceLinkDataSource{}
)

func NewServiceLinkDataSource() datasource.DataSource {
	return &serviceLinkDataSource{}
}

type serviceLinkDataSource struct {
	p *providerData
}

func (d *serviceLinkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_link"
}

func (d *serviceLinkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	endpoint := func(desc string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{
			Computed:            true,
			MarkdownDescription: desc,
			Attributes: map[string]schema.Attribute{
				"kind": schema.StringAttribute{Computed: true, MarkdownDescription: "Kind of service."},
				"name": schema.StringAttribute{Computed: true, MarkdownDescription: "The service's slug."},
			},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a declared network path between two services by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env": schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the link belongs to."},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The link's name — `<consumer>-<target>`, as reported by the console, " +
					"`hfctl service-links list`, or a `hyperfluid_service_link` resource's `name`.",
			},
			"id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Composite identifier `env/name`."},
			"consumer": endpoint("The service permitted to reach the target."),
			"target":   endpoint("The service made reachable."),
			"target_ports": schema.SetNestedAttribute{
				Computed: true,
				MarkdownDescription: "Always null on a data source: the API reports the ports it opened " +
					"(`ports`), not which of them were explicitly asked for.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Port number."},
						"protocol": schema.StringAttribute{Computed: true, MarkdownDescription: "L4 protocol."},
					},
				},
			},
			"ports": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The ports opened for this link. Empty until it has been reconciled.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Port number."},
						"protocol": schema.StringAttribute{Computed: true, MarkdownDescription: "L4 protocol."},
					},
				},
			},
			"ready": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the platform has opened the ports."},
		},
	}
}

func (d *serviceLinkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *serviceLinkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg serviceLinkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env := cfg.Env.ValueString()
	link, err := d.p.API.FindServiceLink(ctx, d.p.OrgID, env, cfg.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Service link not found",
			"No service link named "+cfg.Name.ValueString()+" in this environment.")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read service link", err.Error())
		return
	}
	state := toServiceLinkModel(env, link)
	state.TargetPorts = nil
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
