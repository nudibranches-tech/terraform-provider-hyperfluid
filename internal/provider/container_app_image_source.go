// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// container_app_image_source.go — the `image_source { git { … } }` block of
// hyperfluid_container_app (ADR-0052). A Git source replaces the literal
// image: the platform reads `hyperfluid.toml` from the repository and deploys
// the image it declares. What it resolved is reported through computed
// attributes, never through image_repository / image_tag, so a deploy driven by
// Git never shows up as drift.

// minGitInterval is the shortest check interval the platform accepts.
const minGitInterval = 5 * time.Minute

// gitSourceModel is the `git` block. Every optional field stays null when the
// user leaves it out: the platform default is resolved server-side and is never
// written back, so a plan can never persist one into the spec.
type gitSourceModel struct {
	Provider           types.String `tfsdk:"provider"`
	Repository         types.String `tfsdk:"repository"`
	BaseURL            types.String `tfsdk:"base_url"`
	Credential         types.String `tfsdk:"credential"`
	Branch             types.String `tfsdk:"branch"`
	TagPattern         types.String `tfsdk:"tag_pattern"`
	Path               types.String `tfsdk:"path"`
	Container          types.String `tfsdk:"container"`
	Interval           types.String `tfsdk:"interval"`
	AlertOnSyncFailure types.Bool   `tfsdk:"alert_on_sync_failure"`
}

func gitSourceAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"provider":              types.StringType,
		"repository":            types.StringType,
		"base_url":              types.StringType,
		"credential":            types.StringType,
		"branch":                types.StringType,
		"tag_pattern":           types.StringType,
		"path":                  types.StringType,
		"container":             types.StringType,
		"interval":              types.StringType,
		"alert_on_sync_failure": types.BoolType,
	}
}

func imageSourceAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"git": types.ObjectType{AttrTypes: gitSourceAttrTypes()},
	}
}

func nullImageSource() types.Object {
	return types.ObjectNull(imageSourceAttrTypes())
}

// gitProviders are the hosts the platform can read a repository from.
var gitProviders = []string{
	string(console.GitProviderGithub),
	string(console.GitProviderGitlab),
	string(console.GitProviderForgejo),
}

const gitSourceDescription = "Deploy the image a Git repository declares instead of a literal `image_repository` / " +
	"`image_tag`, which it conflicts with. See \"Deploying from Git\" above."

// imageSourceBlock is the schema of `image_source`, with its single `git` block.
func imageSourceBlock() schema.Block {
	return schema.SingleNestedBlock{
		MarkdownDescription: gitSourceDescription,
		Validators: []validator.Object{
			objectvalidator.ConflictsWith(path.MatchRoot("image_repository"), path.MatchRoot("image_tag")),
		},
		Blocks: map[string]schema.Block{
			"git": schema.SingleNestedBlock{
				MarkdownDescription: "A Git repository holding the `hyperfluid.toml` that declares the image. " +
					"Required when `image_source` is set.",
				Attributes: map[string]schema.Attribute{
					// provider, repository and credential are required, but only checked
					// in ValidateConfig: the framework demands a Required attribute of a
					// single nested block even when the block itself is absent.
					"provider": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Git host: `github`, `gitlab` or `forgejo`. Required.",
						Validators:          []validator.String{stringvalidator.OneOf(gitProviders...)},
					},
					"repository": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Repository path on the provider, e.g. `acme/orders-api`. " +
							"For GitLab, the full group path. Required.",
						Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"base_url": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Origin of a self-hosted provider (GitHub Enterprise, self-managed GitLab, " +
							"external Forgejo). Leave it out for the provider's public host, or for the " +
							"organization's own Forgejo.",
						Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"credential": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Name of an organization-scoped `hyperfluid_secret` of type " +
							"`scm_credential` the platform reads the repository with. Required, public repositories " +
							"included: the checks then count against the token's own rate limit rather than the " +
							"anonymous one every app of the cluster shares. A read-only token is enough " +
							"(GitHub fine-grained Contents: read, GitLab `read_repository`, Forgejo " +
							"`read:repository`).",
						Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"branch": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Follow the head of this branch. Conflicts with `tag_pattern`. When " +
							"neither is set, the repository's default branch is followed.",
						Validators: []validator.String{
							stringvalidator.LengthAtLeast(1),
							stringvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName("tag_pattern")),
						},
					},
					"tag_pattern": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Follow the highest tag matching this regular expression, which must " +
							"capture the version in a group named `version`, e.g. `v?(?<version>\\d+\\.\\d+\\.\\d+)`. " +
							"It is matched against the whole tag, and the file is read at that tag. Conflicts with " +
							"`branch`.",
						Validators: []validator.String{
							stringvalidator.LengthBetween(1, 256),
							stringvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName("branch")),
						},
					},
					"path": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Path of the file in the repository. Defaults to `hyperfluid.toml`.",
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"container": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Key of the `[containers.<key>]` entry this app reads. Defaults to the " +
							"app's name.",
						Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"interval": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "How often the repository is checked, as a duration such as `5m`, " +
							"`15m`, `1h` or `24h`. At least `5m`, which is also the default.",
						Validators: []validator.String{intervalValidator{}},
					},
					"alert_on_sync_failure": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Whether a failing check raises the default \"Git sync failed\" alert. " +
							"The platform raises it unless this is `false`.",
					},
				},
			},
		},
	}
}

// gitStatusAttributes are the computed attributes reporting what a Git source
// resolved. They are only ever planned as unknown while an image_source is
// configured (gitStatusModifier), so apps without one never show them in a plan.
func gitStatusAttributes() map[string]schema.Attribute {
	mod := []planmodifier.String{gitStatusModifier{}}
	return map[string]schema.Attribute{
		"resolved_image": schema.StringAttribute{
			Computed: true, PlanModifiers: mod,
			MarkdownDescription: "The image a Git `image_source` resolved and runs, as `repository:tag@digest`. " +
				"Null until the first successful check, and for an app without a Git source.",
		},
		"resolved_ref": schema.StringAttribute{
			Computed: true, PlanModifiers: mod,
			MarkdownDescription: "The branch followed, or the tag `tag_pattern` selected.",
		},
		"revision": schema.StringAttribute{
			Computed: true, PlanModifiers: mod,
			MarkdownDescription: "The commit the running image was read from.",
		},
		"last_synced_at": schema.StringAttribute{
			Computed: true, PlanModifiers: mod,
			MarkdownDescription: "When the repository was last checked successfully (RFC 3339), whether or not " +
				"anything changed.",
		},
		"sync_error": schema.StringAttribute{
			Computed: true, PlanModifiers: mod,
			MarkdownDescription: "Why the last check failed, or null. A failing check never stops the app: it keeps " +
				"running the last good image.",
		},
	}
}

// gitStatusModifier plans the computed Git status as null for an app that has no
// image_source, instead of "known after apply" on every unrelated update. With a
// source it stays unknown: the platform moves these on its own.
type gitStatusModifier struct{}

func (gitStatusModifier) Description(context.Context) string {
	return "Null unless an image_source is configured."
}

func (m gitStatusModifier) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (gitStatusModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.PlanValue.IsUnknown() {
		return
	}
	var src types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("image_source"), &src)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if src.IsNull() {
		resp.PlanValue = types.StringNull()
	}
}

// ── interval ──────────────────────────────────────────────────────────────

type intervalValidator struct{}

func (intervalValidator) Description(context.Context) string {
	return "a duration of at least 5m, in whole seconds"
}

func (v intervalValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (intervalValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := parseInterval(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid interval", err.Error())
	}
}

// parseInterval turns a duration such as "15m" into the whole seconds the API
// takes.
func parseInterval(s string) (int32, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration such as \"5m\", \"1h\" or \"24h\"", s)
	}
	if d < minGitInterval {
		return 0, fmt.Errorf("%q is below the %s minimum", s, minGitInterval)
	}
	if d%time.Second != 0 || d/time.Second > math.MaxInt32 {
		return 0, fmt.Errorf("%q must be a whole number of seconds, at most %d", s, math.MaxInt32)
	}
	return int32(d / time.Second), nil
}

// formatInterval is parseInterval's inverse, with the zero units time.Duration
// prints dropped ("15m0s" → "15m").
func formatInterval(secs int32) string {
	s := (time.Duration(secs) * time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// ── config → API ──────────────────────────────────────────────────────────

// gitSourceFromObject reads the configured block; nil when there is none.
func gitSourceFromObject(ctx context.Context, obj types.Object) (*gitSourceModel, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}
	gitAttr, ok := obj.Attributes()["git"].(types.Object)
	if !ok || gitAttr.IsNull() || gitAttr.IsUnknown() {
		return nil, nil
	}
	var m gitSourceModel
	d := gitAttr.As(ctx, &m, basetypes.ObjectAsOptions{})
	if d.HasError() {
		return nil, d
	}
	return &m, d
}

// buildImageSource maps the block onto the API. A null field is omitted from
// the request, so the platform default is never written into the spec.
func buildImageSource(m gitSourceModel) (console.ImageSource, error) {
	git := console.GitImageSource{
		Provider:   console.GitProvider(m.Provider.ValueString()),
		Repository: m.Repository.ValueString(),
		BaseUrl:    stringPtr(m.BaseURL),
		Path:       stringPtr(m.Path),
		Container:  stringPtr(m.Container),
		Credential: console.SecretManagerRef{
			Name: m.Credential.ValueString(),
			Type: console.SecretManagerTypeControlplane,
		},
	}
	if !m.Interval.IsNull() && !m.Interval.IsUnknown() {
		secs, err := parseInterval(m.Interval.ValueString())
		if err != nil {
			return console.ImageSource{}, err
		}
		git.IntervalSeconds = &secs
	}

	var ref console.GitRef
	switch {
	case !m.Branch.IsNull() && !m.Branch.IsUnknown():
		if err := ref.FromGitRef0(console.GitRef0{Name: m.Branch.ValueString(), Type: console.GitRef0TypeBranch}); err != nil {
			return console.ImageSource{}, err
		}
		git.Ref = &ref
	case !m.TagPattern.IsNull() && !m.TagPattern.IsUnknown():
		if err := ref.FromGitRef1(console.GitRef1{Pattern: m.TagPattern.ValueString(), Type: console.GitRef1TypeTag}); err != nil {
			return console.ImageSource{}, err
		}
		git.Ref = &ref
	}

	var src console.ImageSource
	if err := src.FromImageSource0(console.ImageSource0{Git: git}); err != nil {
		return console.ImageSource{}, err
	}
	return src, nil
}

// ── API → state ───────────────────────────────────────────────────────────

// gitSourceToObject maps what the API holds back onto the block. prior is the
// planned or previous block: it keeps the user's spelling where the API stores
// an equivalent one, and tells a platform default from a deliberate choice for
// alert_on_sync_failure, which the API always reports.
func gitSourceToObject(src *console.ImageSource, alert *bool, prior *gitSourceModel) (types.Object, error) {
	if src == nil {
		return nullImageSource(), nil
	}
	wrapper, err := src.AsImageSource0()
	if err != nil {
		return types.Object{}, fmt.Errorf("decode image source: %w", err)
	}
	git := wrapper.Git

	m := gitSourceModel{
		Provider:   types.StringValue(string(git.Provider)),
		Repository: types.StringValue(git.Repository),
		BaseURL:    optString(git.BaseUrl),
		Path:       optString(git.Path),
		Container:  optString(git.Container),
		Branch:     types.StringNull(),
		TagPattern: types.StringNull(),
		Interval:   types.StringNull(),
		Credential: types.StringValue(git.Credential.Name),
	}
	if prior != nil && git.BaseUrl != nil && !prior.BaseURL.IsNull() &&
		strings.EqualFold(strings.TrimRight(prior.BaseURL.ValueString(), "/"), strings.TrimRight(*git.BaseUrl, "/")) {
		m.BaseURL = prior.BaseURL
	}

	if git.Ref != nil {
		raw, err := git.Ref.MarshalJSON()
		if err != nil {
			return types.Object{}, fmt.Errorf("decode git ref: %w", err)
		}
		var ref struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(raw, &ref); err != nil {
			return types.Object{}, fmt.Errorf("decode git ref: %w", err)
		}
		switch ref.Type {
		case string(console.GitRef0TypeBranch):
			m.Branch = types.StringValue(ref.Name)
		case string(console.GitRef1TypeTag):
			m.TagPattern = types.StringValue(ref.Pattern)
		}
	}

	if git.IntervalSeconds != nil {
		m.Interval = types.StringValue(formatInterval(*git.IntervalSeconds))
		if prior != nil && !prior.Interval.IsNull() {
			if secs, err := parseInterval(prior.Interval.ValueString()); err == nil && secs == *git.IntervalSeconds {
				m.Interval = prior.Interval
			}
		}
	}

	// The API reports the alert as on when the user never chose: that is the
	// platform default, not something to write into the state.
	m.AlertOnSyncFailure = types.BoolNull()
	if alert != nil && (!*alert || (prior != nil && !prior.AlertOnSyncFailure.IsNull())) {
		m.AlertOnSyncFailure = types.BoolValue(*alert)
	}

	gitObj, d := types.ObjectValueFrom(context.Background(), gitSourceAttrTypes(), m)
	if d.HasError() {
		return types.Object{}, fmt.Errorf("build git block: %v", d.Errors())
	}
	obj, d := types.ObjectValue(imageSourceAttrTypes(), map[string]attr.Value{"git": gitObj})
	if d.HasError() {
		return types.Object{}, fmt.Errorf("build image_source block: %v", d.Errors())
	}
	return obj, nil
}

// gitStatus is what a Git source resolved, flattened onto the computed attributes.
type gitStatus struct {
	ResolvedImage types.String
	ResolvedRef   types.String
	Revision      types.String
	LastSyncedAt  types.String
	SyncError     types.String
}

func nullGitStatus() gitStatus {
	return gitStatus{
		ResolvedImage: types.StringNull(),
		ResolvedRef:   types.StringNull(),
		Revision:      types.StringNull(),
		LastSyncedAt:  types.StringNull(),
		SyncError:     types.StringNull(),
	}
}

func gitStatusFrom(st *console.ImageSourceStatus) gitStatus {
	out := nullGitStatus()
	if st == nil {
		return out
	}
	if st.Resolved != nil {
		out.ResolvedImage = types.StringValue(renderResolvedImage(*st.Resolved))
	}
	out.ResolvedRef = optNonEmpty(st.ResolvedRef)
	out.Revision = optNonEmpty(st.Revision)
	out.LastSyncedAt = optNonEmpty(st.LastSyncedAt)
	out.SyncError = optNonEmpty(st.Error)
	return out
}

func optNonEmpty(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// renderResolvedImage prints an image the way the pod runs it: the digest wins
// over the tag, which stays for display.
func renderResolvedImage(r console.ResolvedImage) string {
	out := r.Repository
	if r.Tag != nil && *r.Tag != "" {
		out += ":" + *r.Tag
	}
	if r.Digest != nil && *r.Digest != "" {
		out += "@" + *r.Digest
	}
	return out
}
