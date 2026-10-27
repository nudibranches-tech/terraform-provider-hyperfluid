// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// hyperfluid_container_app data source — look up an existing container app by
// slug within an environment. Reuses the resource's containerAppModel + readInto
// so spec/status mapping lives in one place.

var (
	_ datasource.DataSource                     = &containerAppDataSource{}
	_ datasource.DataSourceWithConfigure        = &containerAppDataSource{}
	_ datasource.DataSourceWithConfigValidators = &containerAppDataSource{}
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
		MarkdownDescription: "Look up an existing container app by its slug within an environment.",
		Attributes: map[string]schema.Attribute{
			"env": schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the app runs in."},
			"slug": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Slug of the app to look up. Display names are not unique, so an app is looked up " +
					"by its slug. This is the name a `hyperfluid_service_link` endpoint takes.",
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "The app's slug, under its former name. Use `slug` instead.",
				DeprecationMessage:  "Use slug instead.",
			},
			"display_name":       cs("The app's name, shown in the console."),
			"description":        cs("Free-form description."),
			"tags":               schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"id":                 cs("App id."),
			"image_repository":   cs("Container image repository."),
			"image_tag":          cs("Container image tag."),
			"port":               ci("The app's single container port. Deprecated by the API in favour of `ports`; for an app publishing several, this reports the primary one."),
			"replicas":           ci("Desired replica count."),
			"enabled":            schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the app is running."},
			"expose_to_internet": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether internet-facing routes are created for the app."},
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
			"image_source": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "The Git source the app's image comes from, or null for an app with a literal " +
					"image. `image_repository` and `image_tag` then report the image it resolved, when it has one.",
				Attributes: map[string]schema.Attribute{
					"git": schema.SingleNestedAttribute{
						Computed:            true,
						MarkdownDescription: "The repository holding the `hyperfluid.toml` that declares the image.",
						Attributes: map[string]schema.Attribute{
							"provider":              cs("Git host: `github`, `gitlab` or `forgejo`."),
							"repository":            cs("Repository path on the provider."),
							"base_url":              cs("Origin of a self-hosted provider, or null for the public host."),
							"credential":            cs("Name of the `scm_credential` secret used to read the repository, or null for a public one."),
							"branch":                cs("The branch followed, or null."),
							"tag_pattern":           cs("The regular expression selecting the tag followed, or null."),
							"path":                  cs("Path of the file in the repository, or null for `hyperfluid.toml`."),
							"container":             cs("Key of the entry the app reads, or null for the app's name."),
							"interval":              cs("How often the repository is checked, or null for the platform default."),
							"sync_policy":           cs("`auto` or `manual`, or null for the platform default (`auto`)."),
							"alert_on_sync_failure": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether a failing check raises an alert, or null for the platform default."},
						},
					},
				},
			},
			"resolved_image": cs("The image a Git source resolved and runs, as `repository:tag@digest`."),
			"resolved_ref":   cs("The branch followed, or the tag a Git source's `tag_pattern` selected."),
			"revision":       cs("The commit the running image was read from."),
			"last_synced_at": cs("When the repository was last checked successfully (RFC 3339)."),
			"sync_error":     cs("Why the last check failed, or null."),
			"ports": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Every port the app publishes.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":     cs("Port name, or null for an app still on the single-`port` form."),
						"port":     ci("Port the container listens on."),
						"protocol": cs("One of `HTTP`, `TCP` or `UDP`."),
						"primary":  schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether public routes and the default health probe target this port."},
					},
				},
			},
			"env_vars": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Plain environment variables of the main container, in order.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":  cs("Variable name."),
						"value": cs("Variable value."),
					},
				},
			},
			"secret_refs": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Environment variables read from an organization-wide secret when the app is deployed.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"env_var_name": cs("Variable name."),
						"secret_name":  cs("Name of the secret holding the value."),
					},
				},
			},
			"image_pull_secrets": schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Names of the secrets used to pull the app's images."},
			"command":            schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Entrypoint of the main container, or empty for the image's."},
			"args":               schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Arguments of the main container, or empty for the image's."},
			"init_containers": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Containers run to completion, in order, before the main container starts.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":              cs("Container name."),
						"image_repository":  cs("Image repository, or null when the container runs the app's own image."),
						"image_tag":         cs("Image tag, or null when the container runs the app's own image."),
						"command":           schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Entrypoint, or null for the image's."},
						"args":              schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Arguments, or null for the image's."},
						"inherit_env":       schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the container gets the app's environment."},
						"mount_persistence": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the app's persistent volumes are mounted."},
					},
				},
			},
			"file_mounts": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Files written into the main container before it starts.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"mount_path":  cs("Path of the file."),
						"mode":        cs("Octal file mode, or null for the platform's default."),
						"content":     cs("Inline content, or null for a secret-backed file."),
						"secret_name": cs("Secret holding the file, or null for inline content."),
					},
				},
			},
			"custom_resources": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Always null on a data source: a tier and a custom size cannot be told apart " +
					"from the app. Read `cpu_request` and `memory_request` instead.",
				Attributes: map[string]schema.Attribute{
					"cpu_millis": ci("CPU sold, in millicores."),
					"memory_mib": ci("Memory, in MiB."),
				},
			},
			"resource_mode": cs("What the console does with the CPU rightsizing recommendation: `advisory`, `manual` or `managed`."),
			"autoscaling": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Horizontal autoscaling, or null when the app runs a fixed `replicas`.",
				Attributes: map[string]schema.Attribute{
					"min_replicas":                ci("Fewest replicas, or null for the platform's 1."),
					"max_replicas":                ci("Most replicas."),
					"target_cpu_utilization":      ci("CPU use held, in percent of the request."),
					"target_memory_utilization":   ci("Memory use held, in percent of the request."),
					"target_http_concurrency":     ci("In-flight requests per replica held."),
					"scale_to_zero_after_seconds": ci("Idle time before the app sleeps."),
				},
			},
			"metrics": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Where the app serves Prometheus metrics, or null.",
				Attributes: map[string]schema.Attribute{
					"port": cs("Name of the port serving the metrics."),
					"path": cs("Path scraped, or null for the platform's default."),
				},
			},
			"custom_domains": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Domains of the organization's own the app answers on.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"fqdn":            cs("The domain."),
						"tls_mode":        cs("`acme` or `secret`."),
						"tls_secret_name": cs("Secret holding the certificate, with `tls_mode = \"secret\"`."),
					},
				},
			},
			"custom_domain_statuses": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Where each custom domain stands, as the platform last reported it.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"fqdn":                    cs("The domain."),
						"state":                   cs("`pendingVerification`, `conflict`, `pendingDns`, `provisioning`, `ready` or `error`."),
						"message":                 cs("Why the domain is not ready, when it is not."),
						"target":                  cs("Where the domain's DNS record must point."),
						"observed_addresses":      schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Addresses the domain resolved to at the last check."},
						"certificate_secret_name": cs("Secret holding the certificate served."),
					},
				},
			},
			"image_digest":    cs("Digest the main image is pinned to, when it is."),
			"tag_is_floating": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the image tag is one upstream can re-push."},
		},
	}
}

func (d *containerAppDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("slug"), path.MatchRoot("name")),
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
	slug := cfg.Slug.ValueString()
	if cfg.Slug.IsNull() {
		slug = cfg.Name.ValueString()
	}
	appID, err := d.p.API.FindContainerAppIDBySlug(ctx, d.p.OrgID, env, slug)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Container app not found", "no container app with slug "+slug+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up container app", err.Error())
		return
	}

	// Reuse the resource's mapper so spec/status → model lives in one place; it
	// only needs the API client, so a zero-value resource with our providerData is
	// enough. resource_tier isn't returned by the API → null on a data source.
	state, err := (&containerAppResource{p: d.p}).readInto(ctx, appID, importedApp())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read container app", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
