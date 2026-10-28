// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

var (
	_ datasource.DataSource              = &virtualMachineDataSource{}
	_ datasource.DataSourceWithConfigure = &virtualMachineDataSource{}
	_ datasource.DataSource              = &virtualMachineImagesDataSource{}
	_ datasource.DataSourceWithConfigure = &virtualMachineImagesDataSource{}
)

func NewVirtualMachineDataSource() datasource.DataSource {
	return &virtualMachineDataSource{}
}

type virtualMachineDataSource struct {
	p *providerData
}

type virtualMachineDataSourceModel struct {
	ID                 types.String `tfsdk:"id"`
	Env                types.String `tfsdk:"env"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	Tags               types.List   `tfsdk:"tags"`
	CPU                types.String `tfsdk:"cpu"`
	Memory             types.String `tfsdk:"memory"`
	Disk               types.String `tfsdk:"disk"`
	ImageSourceRef     types.String `tfsdk:"image_source_ref"`
	ImageKey           types.String `tfsdk:"image_key"`
	UserDataSecretName types.String `tfsdk:"user_data_secret_name"`
	Running            types.Bool   `tfsdk:"running"`
	Slug               types.String `tfsdk:"slug"`
	Phase              types.String `tfsdk:"phase"`
	IPAddress          types.String `tfsdk:"ip_address"`
	InstallMedia       types.String `tfsdk:"install_media"`
}

func (d *virtualMachineDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_machine"
}

func (d *virtualMachineDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a virtual machine by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":                   schema.StringAttribute{Required: true, MarkdownDescription: "Environment id."},
			"name":                  schema.StringAttribute{Required: true, MarkdownDescription: "Virtual machine name."},
			"id":                    cs("Virtual machine id."),
			"description":           cs("Free-form description."),
			"tags":                  schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"cpu":                   cs("Number of vCPUs."),
			"memory":                cs("Guest memory."),
			"disk":                  cs("Root disk size."),
			"image_source_ref":      cs("Image source name."),
			"image_key":             cs("Image key."),
			"user_data_secret_name": cs("Secret holding the cloud-init user-data, if any."),
			"running":               schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the virtual machine is meant to run."},
			"slug":                  cs("Derived slug."),
			"phase":                 cs("Lifecycle phase."),
			"ip_address":            cs("Primary IP address of the guest."),
			"install_media":         cs("`Attached` or `Removed` for an installer image; null for a disk image."),
		},
	}
}

func (d *virtualMachineDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *virtualMachineDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg virtualMachineDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, name := cfg.Env.ValueString(), cfg.Name.ValueString()
	vm, err := d.p.API.FindVirtualMachine(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Virtual machine not found", "no virtual machine named "+name+" in environment "+env)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to look up virtual machine", featureVMs, err)
		return
	}
	m, diags := virtualMachineFrom(ctx, vm)
	resp.Diagnostics.Append(diags...)
	out := virtualMachineDataSourceModel{
		ID: m.ID, Env: m.Env, Name: m.Name, Description: m.Description, Tags: m.Tags, CPU: m.CPU, Memory: m.Memory,
		Disk: m.Disk, ImageSourceRef: m.ImageSourceRef, ImageKey: m.ImageKey, UserDataSecretName: m.UserDataSecretName,
		Running: m.Running, Slug: m.Slug, Phase: m.Phase, IPAddress: m.IPAddress, InstallMedia: m.InstallMedia,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

// ── Image catalog ──────────────────────────────────────────────────────────

func NewVirtualMachineImagesDataSource() datasource.DataSource {
	return &virtualMachineImagesDataSource{}
}

type virtualMachineImagesDataSource struct {
	p *providerData
}

type virtualMachineImagesModel struct {
	Env    types.String `tfsdk:"env"`
	Images types.List   `tfsdk:"images"`
}

type virtualMachineImageModel struct {
	SourceRef   types.String `tfsdk:"image_source_ref"`
	Key         types.String `tfsdk:"image_key"`
	Description types.String `tfsdk:"description"`
	Kind        types.String `tfsdk:"kind"`
}

var virtualMachineImageType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"image_source_ref": types.StringType, "image_key": types.StringType, "description": types.StringType, "kind": types.StringType,
}}

func (d *virtualMachineImagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_machine_images"
}

func (d *virtualMachineImagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The images an environment can boot a virtual machine from. Reading it needs the " +
			"`virtual_machine:create` permission.",
		Attributes: map[string]schema.Attribute{
			"env": schema.StringAttribute{Required: true, MarkdownDescription: "Environment id."},
			"images": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Enabled images, each with the `image_source_ref` and `image_key` a virtual machine takes.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"image_source_ref": schema.StringAttribute{Computed: true, MarkdownDescription: "Image source name."},
					"image_key":        schema.StringAttribute{Computed: true, MarkdownDescription: "Image key."},
					"description":      schema.StringAttribute{Computed: true, MarkdownDescription: "What the platform team wrote about the image."},
					"kind":             schema.StringAttribute{Computed: true, MarkdownDescription: "`disk` (boots as is) or `installer` (installs onto a blank disk)."},
				}},
			},
		},
	}
}

func (d *virtualMachineImagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *virtualMachineImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg virtualMachineImagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	catalog, err := d.p.API.GetVirtualMachineImageCatalog(ctx, d.p.OrgID, cfg.Env.ValueString())
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read the virtual machine image catalog", featureVMs, err)
		return
	}
	images := []virtualMachineImageModel{}
	for _, src := range catalog.Sources {
		for _, img := range src.Images {
			kind := types.StringNull()
			if img.Kind != nil {
				kind = types.StringValue(string(*img.Kind))
			}
			images = append(images, virtualMachineImageModel{
				SourceRef: types.StringValue(src.Name), Key: types.StringValue(img.Key),
				Description: types.StringValue(img.Description), Kind: kind,
			})
		}
	}
	list, diags := types.ListValueFrom(ctx, virtualMachineImageType, images)
	resp.Diagnostics.Append(diags...)
	cfg.Images = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
