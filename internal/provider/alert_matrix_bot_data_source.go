// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &alertMatrixBotDataSource{}
	_ datasource.DataSourceWithConfigure = &alertMatrixBotDataSource{}
)

func NewAlertMatrixBotDataSource() datasource.DataSource {
	return &alertMatrixBotDataSource{}
}

type alertMatrixBotDataSource struct {
	p *providerData
}

type alertMatrixBotDataSourceModel struct {
	Name                  types.String `tfsdk:"name"`
	ID                    types.String `tfsdk:"id"`
	Preset                types.String `tfsdk:"preset"`
	HomeserverURL         types.String `tfsdk:"homeserver_url"`
	UserID                types.String `tfsdk:"user_id"`
	AllowUnencryptedRooms types.Bool   `tfsdk:"allow_unencrypted_rooms"`
	Phase                 types.String `tfsdk:"phase"`
	DeviceID              types.String `tfsdk:"device_id"`
	Message               types.String `tfsdk:"message"`
	ReloginRequired       types.Bool   `tfsdk:"relogin_required"`
}

func (d *alertMatrixBotDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_matrix_bot"
}

func (d *alertMatrixBotDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a Matrix alert bot by name, e.g. to name it from a `matrix` alert channel " +
			"managed elsewhere. " + alertingFeatureNote,
		Attributes: map[string]schema.Attribute{
			"name":           schema.StringAttribute{Required: true, MarkdownDescription: "Bot name to look up."},
			"id":             schema.StringAttribute{Computed: true, MarkdownDescription: "Bot id, named by a channel's `matrix.bot_id`."},
			"preset":         schema.StringAttribute{Computed: true, MarkdownDescription: "`tchap` or `generic`."},
			"homeserver_url": schema.StringAttribute{Computed: true, MarkdownDescription: "The homeserver of a `generic` bot."},
			"user_id":        schema.StringAttribute{Computed: true, MarkdownDescription: "The bot account's MXID."},
			"allow_unencrypted_rooms": schema.BoolAttribute{
				Computed: true, MarkdownDescription: "Whether the bot may post into a room that is not end-to-end encrypted.",
			},
			"phase":     schema.StringAttribute{Computed: true, MarkdownDescription: "Where the bot's notifier stands."},
			"device_id": schema.StringAttribute{Computed: true, MarkdownDescription: "The Matrix device the bot lives on."},
			"message":   schema.StringAttribute{Computed: true, MarkdownDescription: "What the phase is about."},
			"relogin_required": schema.BoolAttribute{
				Computed: true, MarkdownDescription: "The bot's session ended and it must log in again.",
			},
		},
	}
}

func (d *alertMatrixBotDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *alertMatrixBotDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg alertMatrixBotDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	bot, err := d.p.API.FindAlertMatrixBotByName(ctx, d.p.OrgID, cfg.Name.ValueString())
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to look up Matrix bot", err)
		return
	}
	m := alertMatrixBotFrom(bot, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, alertMatrixBotDataSourceModel{
		Name: m.Name, ID: m.ID, Preset: m.Preset, HomeserverURL: m.HomeserverURL, UserID: m.UserID,
		AllowUnencryptedRooms: m.AllowUnencryptedRooms, Phase: m.Phase, DeviceID: m.DeviceID, Message: m.Message,
		ReloginRequired: m.ReloginRequired,
	})...)
}
