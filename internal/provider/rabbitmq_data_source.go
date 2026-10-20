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

// hyperfluid_rabbitmq data source — look up an existing broker by name within
// an environment. Reuses the resource's model + readInto.

var (
	_ datasource.DataSource              = &rabbitMQDataSource{}
	_ datasource.DataSourceWithConfigure = &rabbitMQDataSource{}
)

func NewRabbitMQDataSource() datasource.DataSource {
	return &rabbitMQDataSource{}
}

type rabbitMQDataSource struct {
	p *providerData
}

func (d *rabbitMQDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rabbitmq"
}

func (d *rabbitMQDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	quantity := func(desc string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{
			Computed:            true,
			MarkdownDescription: desc,
			Attributes: map[string]schema.Attribute{
				"cpu":    cs("CPU quantity."),
				"memory": cs("Memory quantity."),
			},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing RabbitMQ broker by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":      schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the broker runs in."},
			"name":     schema.StringAttribute{Required: true, MarkdownDescription: "Broker name."},
			"id":       cs("Broker id."),
			"replicas": schema.Int64Attribute{Computed: true, MarkdownDescription: "Pinned node count; null when the broker tracks the platform default."},
			"storage":  cs("Pinned volume size per node; null when the broker tracks the platform default."),
			"resources": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Pinned CPU and memory requests and limits per node; null when the broker tracks the platform default.",
				Attributes: map[string]schema.Attribute{
					"requests": quantity("Resource requests."),
					"limits":   quantity("Resource limits."),
				},
			},
			"plugins":                    schema.SetAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Enabled optional plugins."},
			"image":                      cs("Pinned image; null when the broker tracks the platform default."),
			"description":                cs("Free-form description."),
			"tags":                       schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"host":                       cs("Connection host."),
			"amqp_port":                  schema.Int64Attribute{Computed: true, MarkdownDescription: "AMQP port."},
			"management_port":            schema.Int64Attribute{Computed: true, MarkdownDescription: "Management (HTTP API) port."},
			"phase":                      cs("Current lifecycle phase."),
			"slug":                       cs("Derived slug."),
			"username_secret_name":       cs("Secret-manager name of the broker username."),
			"password_secret_name":       cs("Secret-manager name of the broker password."),
			"amqp_uri_secret_name":       cs("Secret-manager name of the AMQP connection URI."),
			"management_uri_secret_name": cs("Secret-manager name of the management API URI."),
		},
	}
}

func (d *rabbitMQDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *rabbitMQDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg rabbitMQModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.Name.ValueString()
	id, err := d.p.API.FindRabbitMQ(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("RabbitMQ broker not found", "no broker named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up RabbitMQ broker", err.Error())
		return
	}

	// Reuse the resource's mapper (it only needs the API client) so the model
	// mapping lives in one place.
	state, err := (&rabbitMQResource{p: d.p}).readInto(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read RabbitMQ broker", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
