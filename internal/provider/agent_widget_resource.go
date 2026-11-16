// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// agent_widget_resource.go — the public web widget of an agent: the one
// `<script>` tag that puts the agent on any site. It is its own resource, not a
// block of hyperfluid_agent, because the console gives it its own life cycle:
// enable (PUT), rotate and disable are separate routes from the agent's PATCH,
// and the outputs a site template needs (the widget id, the script paths) only
// exist once it is enabled. Destroying it disables it; the agent stays.

var (
	_ resource.Resource                   = &agentWidgetResource{}
	_ resource.ResourceWithConfigure      = &agentWidgetResource{}
	_ resource.ResourceWithImportState    = &agentWidgetResource{}
	_ resource.ResourceWithValidateConfig = &agentWidgetResource{}
)

func NewAgentWidgetResource() resource.Resource {
	return &agentWidgetResource{}
}

type agentWidgetResource struct {
	p *providerData
}

type agentWidgetModel struct {
	ID      types.String `tfsdk:"id"`
	AgentID types.String `tfsdk:"agent_id"`

	AllowedOrigins        types.Set    `tfsdk:"allowed_origins"`
	AllowAnyOrigin        types.Bool   `tfsdk:"allow_any_origin"`
	Title                 types.String `tfsdk:"title"`
	Greeting              types.String `tfsdk:"greeting"`
	Colour                types.String `tfsdk:"colour"`
	Suggestions           types.List   `tfsdk:"suggestions"`
	Lang                  types.String `tfsdk:"lang"`
	PrivacyURL            types.String `tfsdk:"privacy_url"`
	SessionRetentionHours types.Int64  `tfsdk:"session_retention_hours"`
	MaxQuestionLength     types.Int64  `tfsdk:"max_question_length"`
	RotationTrigger       types.String `tfsdk:"rotation_trigger"`

	WidgetID                   types.String `tfsdk:"widget_id"`
	Phase                      types.String `tfsdk:"phase"`
	ScriptFloatingPath         types.String `tfsdk:"script_floating_path"`
	ScriptPinnedPath           types.String `tfsdk:"script_pinned_path"`
	ScriptIntegrity            types.String `tfsdk:"script_integrity"`
	EmbedSnippet               types.String `tfsdk:"embed_snippet"`
	EffectiveMaxQuestionLength types.Int64  `tfsdk:"effective_max_question_length"`
	Warnings                   types.List   `tfsdk:"warnings"`
	IneligibleReason           types.String `tfsdk:"ineligible_reason"`
}

var (
	widgetColourPattern = regexp.MustCompile(`^#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})$`)
	notBlankPattern     = regexp.MustCompile(`\S`)
	httpsURLPattern     = regexp.MustCompile(`^https://[^\s/]+`)
)

func (r *agentWidgetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_widget"
}

func (r *agentWidgetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	text := func(desc string, maxLen int) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, MarkdownDescription: desc,
			Validators: []validator.String{
				stringvalidator.LengthBetween(1, maxLen),
				stringvalidator.RegexMatches(notBlankPattern, "must not be blank"),
			},
		}
	}
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The public web widget of an agent: one `<script>` tag that puts the agent in a chat bubble on any site, " +
			"answering anonymous visitors. Creating it enables the widget, and destroying it disables it (every copy of the tag gets 404); " +
			"the agent itself is untouched. The platform refuses a widget on a capability, on an agent whose tool list is empty or holds " +
			"general `hfctl`, and on an agent whose service account (or a specialist's, for a router) can write. " +
			"Needs the organization's `sapience_agent_enabled` feature.\n\n" +
			"`embed_snippet` is the tag to paste in your site, built from the provider's `endpoint`. The `widget_id` in it is not a secret, " +
			"but it changes when the widget is rotated: rotation is never a side effect, only a new `rotation_trigger` value does it, " +
			"and the tag on your sites must then be updated. Disabling (destroy) and enabling again keeps the same id. " +
			"A widget disabled outside Terraform leaves the state, and the next apply enables it again. " +
			"Changing the configuration later sends all of it again: an argument removed from the configuration is cleared. " +
			"While the widget is on, the platform also refuses agent changes that would make it ineligible (general `hfctl`, an account that can write).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "The agent id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"agent_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "Id of the `hyperfluid_agent`. Changing this forces a new widget.",
				Validators:    []validator.String{isUUID()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"allowed_origins": schema.SetAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Sites allowed to embed the widget, each an origin as a browser sends it: `https://host[:port]`, " +
					"without a path (`http://` only for `localhost`). Required unless `allow_any_origin` is `true`.",
				Validators: []validator.Set{
					setvalidator.SizeBetween(1, 50),
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(
						regexp.MustCompile(`^(https://[^/\s?#*]+|http://(localhost|127\.0\.0\.1|\[::1\])(:\d+)?)/?$`),
						"must be an origin, https://host[:port] (http:// only for localhost), without a path")),
				},
			},
			"allow_any_origin": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Accept any site. Deliberately explicit, and flagged in the console. Defaults to `false`.",
			},
			"title":    text("Title of the chat window, at most 80 characters.", 80),
			"greeting": text("First message visitors see, at most 1000 characters.", 1000),
			"colour": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Accent colour, `#rrggbb`. The platform refuses a colour on which neither dark nor white text reaches WCAG AA contrast.",
				Validators:          []validator.String{stringvalidator.RegexMatches(widgetColourPattern, "must be #rrggbb")},
			},
			"suggestions": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Suggestion chips, in order: at most 6, each at most 200 characters.",
				Validators: []validator.List{
					listvalidator.SizeBetween(1, 6),
					listvalidator.ValueStringsAre(
						stringvalidator.LengthBetween(1, 200),
						stringvalidator.RegexMatches(notBlankPattern, "must not be blank"),
					),
				},
			},
			"lang": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Language of the widget's own copy: `fr` or `en`. Also printed as `data-lang` in `embed_snippet`.",
				Validators:          []validator.String{stringvalidator.OneOf("fr", "en")},
			},
			"privacy_url": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Your privacy notice, an `https://` URL. Required by the platform: the widget processes visitors' " +
					"questions and IP addresses, and links this notice under the input.",
				Validators: []validator.String{stringvalidator.RegexMatches(httpsURLPattern, "must be an https:// URL")},
			},
			"session_retention_hours": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "How long an idle visitor conversation is kept, 1 to 720 hours. Left out, the platform keeps it 24 hours.",
				Validators:          []validator.Int64{int64validator.Between(1, 720)},
			},
			"max_question_length": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Longest question a visitor may ask, 1 to 4000 characters. Left out, the platform allows 800.",
				Validators:          []validator.Int64{int64validator.Between(1, 4000)},
			},
			"rotation_trigger": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Any value; changing it to a new value rotates the widget id, so every copy of the old tag stops " +
					"working at once. Removing it does not rotate. Terraform-only: the platform does not store it. " +
					"A rotation changes `widget_id` and `embed_snippet`, so the tag on your sites must be updated with it.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},

			"widget_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The public widget id (`wa_…`), the tag's `data-agent`. Not a secret: it is in every visitor's page " +
					"source. It changes only on rotation; disabling and enabling again keeps it.",
				PlanModifiers: []planmodifier.String{widgetKeepUnlessRotated(false)},
			},
			"phase": computed("`Active`, or `AgentStopped` while the agent is not running (visitors are told the assistant is unavailable)."),
			"script_floating_path": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Path of the script on the console, `/widget/v1/agent.js`. It follows fixes, and is the one `embed_snippet` uses.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"script_pinned_path": computed("Path of the immutable, versioned script, for a tag with Subresource Integrity. " +
				"It changes on every console upgrade (the console serves only its own build), so a pinned tag must be copied again then; " +
				"it is refreshed on read and never plans a change on its own."),
			"script_integrity": computed("`sha384-…`, the `integrity` attribute of a tag loading `script_pinned_path`. Changes with it."),
			"embed_snippet": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The tag to paste in your site's template: `<script defer src=\"<console>/widget/v1/agent.js\" " +
					"data-agent=\"<widget_id>\" data-lang=\"<lang>\"></script>`, built from the provider's console endpoint. " +
					"It changes when the widget is rotated or `lang` changes.",
				PlanModifiers: []planmodifier.String{widgetKeepUnlessRotated(true)},
			},
			"effective_max_question_length": schema.Int64Attribute{
				Computed: true, MarkdownDescription: "The question cap visitors get, with the platform's default applied.",
			},
			"warnings": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "What to know before exposing this agent to anonymous visitors (what its service account can read, " +
					"a proxy that makes every visitor share one rate limit). They never block; the apply also reports them.",
			},
			"ineligible_reason": computed("Why the platform would refuse a widget on this agent, when it would."),
		},
	}
}

// widgetKeepUnlessRotated keeps the prior value of widget_id (and, with
// followsLang, embed_snippet) through updates that cannot change it, so a site
// template built from it does not show a change on every edit of the widget's
// look. It is unknown when the rotation trigger asks for a new id, and the
// snippet also when lang changes.
func widgetKeepUnlessRotated(followsLang bool) planmodifier.String {
	return widgetKeepModifier{followsLang: followsLang}
}

type widgetKeepModifier struct{ followsLang bool }

func (m widgetKeepModifier) Description(context.Context) string {
	return "Keeps the value unless the widget is rotated."
}

func (m widgetKeepModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m widgetKeepModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || !req.PlanValue.IsUnknown() || req.Plan.Raw.IsNull() {
		return
	}
	var planTrigger, stateTrigger types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("rotation_trigger"), &planTrigger)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("rotation_trigger"), &stateTrigger)...)
	if resp.Diagnostics.HasError() || triggerRequested(planTrigger, stateTrigger) {
		return
	}
	if m.followsLang {
		var planLang, stateLang types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("lang"), &planLang)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("lang"), &stateLang)...)
		if resp.Diagnostics.HasError() || !planLang.Equal(stateLang) {
			return
		}
	}
	resp.PlanValue = req.StateValue
}

// triggerRequested: a rotation or re-login trigger moved to a new value. Unknown counts,
// since it may turn out new; removing the trigger does not rotate.
func triggerRequested(plan, state types.String) bool {
	if plan.IsUnknown() {
		return true
	}
	return !plan.IsNull() && !plan.Equal(state)
}

func (r *agentWidgetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg agentWidgetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.AllowedOrigins.IsUnknown() || cfg.AllowAnyOrigin.IsUnknown() {
		return
	}
	if cfg.AllowedOrigins.IsNull() && !cfg.AllowAnyOrigin.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("allowed_origins"), "No allowed origin",
			"Name the sites allowed to embed the widget in `allowed_origins`, or set `allow_any_origin = true` to accept any site.")
	}
}

func (r *agentWidgetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	r.p = pd
}

func (r *agentWidgetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentWidgetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := widgetConfigRequest(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	agentID := plan.AgentID.ValueString()
	w, err := r.p.API.EnableAiAgentWidget(ctx, r.p.OrgID, agentID, body)
	if err != nil {
		addWidgetError(&resp.Diagnostics, "Failed to enable the agent widget", err)
		return
	}
	state := widgetState(ctx, plan, w, r.p.API.BaseURL(), &resp.Diagnostics)
	state.ID = types.StringValue(agentID)
	state.RotationTrigger = plan.RotationTrigger
	addWidgetWarnings(&resp.Diagnostics, w)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentWidgetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior agentWidgetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w, err := r.p.API.GetAiAgentWidget(ctx, r.p.OrgID, prior.AgentID.ValueString())
	// The agent is gone, or its widget was disabled outside Terraform: either
	// way the widget this resource enabled no longer answers, and the next
	// apply enables it again.
	if errors.Is(err, client.ErrNotFound) || (err == nil && !w.Enabled) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addWidgetError(&resp.Diagnostics, "Failed to read the agent widget", err)
		return
	}
	state := widgetState(ctx, prior, w, r.p.API.BaseURL(), &resp.Diagnostics)
	state.RotationTrigger = prior.RotationTrigger
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentWidgetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior agentWidgetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	agentID := prior.AgentID.ValueString()
	body := widgetConfigRequest(ctx, plan, &resp.Diagnostics)
	before := widgetConfigRequest(ctx, prior, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		w   *console.AiAgentWidgetResponse
		err error
	)
	// PUT replaces the whole configuration: a field left out of it is cleared,
	// which is what removing it from the configuration means.
	if !reflect.DeepEqual(body, before) {
		if w, err = r.p.API.EnableAiAgentWidget(ctx, r.p.OrgID, agentID, body); err != nil {
			addWidgetError(&resp.Diagnostics, "Failed to update the agent widget", err)
			return
		}
	}
	if triggerRequested(plan.RotationTrigger, prior.RotationTrigger) {
		if w, err = r.p.API.RotateAiAgentWidget(ctx, r.p.OrgID, agentID); err != nil {
			addWidgetError(&resp.Diagnostics, "Failed to rotate the agent widget", err)
			return
		}
	}
	if w == nil {
		// Only the trigger was removed: nothing to send, but the outputs are
		// read again like any apply.
		if w, err = r.p.API.GetAiAgentWidget(ctx, r.p.OrgID, agentID); err != nil {
			addWidgetError(&resp.Diagnostics, "Failed to read the agent widget", err)
			return
		}
	}
	state := widgetState(ctx, plan, w, r.p.API.BaseURL(), &resp.Diagnostics)
	state.ID = prior.ID
	state.RotationTrigger = plan.RotationTrigger
	addWidgetWarnings(&resp.Diagnostics, w)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentWidgetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentWidgetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.DisableAiAgentWidget(ctx, r.p.OrgID, state.AgentID.ValueString()); err != nil {
		addWidgetError(&resp.Diagnostics, "Failed to disable the agent widget", err)
	}
}

// ImportState takes the agent id. Only an enabled widget can be imported; the
// rotation trigger is not recovered, and setting one after the import rotates
// at the next apply.
func (r *agentWidgetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !uuidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("expected the agent id (a UUID), got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("agent_id"), req.ID)...)
}

// addWidgetError reports a failed widget call. A refusal is the platform's
// verdict on this agent or this configuration, so it is shown in the
// platform's words, with what usually fixes it.
func addWidgetError(diags *diag.Diagnostics, summary string, err error) {
	var refused *client.RefusedError
	if errors.As(err, &refused) {
		diags.AddError("The platform refused the agent widget",
			refused.Message+"\n\n"+
				"A widget answers anonymous visitors, so the platform refuses it on a capability, on an agent whose tool "+
				"list is empty or holds general `hfctl`, and on an agent whose service account, or a specialist's, has "+
				"write rights (give it a viewer-only account). It also needs an https `privacy_url` and at least one allowed origin.")
		return
	}
	addGatedError(diags, summary, featureAiAgents, err)
}

// addWidgetWarnings surfaces, on the apply that exposes the agent, what the
// platform says the owner should know; they never block.
func addWidgetWarnings(diags *diag.Diagnostics, w *console.AiAgentWidgetResponse) {
	if len(w.Warnings) > 0 {
		diags.AddWarning("Before exposing this agent to anonymous visitors", "- "+strings.Join(w.Warnings, "\n- "))
	}
	if w.Phase == console.AiAgentWidgetPhaseDtoAgentStopped {
		diags.AddWarning("The agent is not running",
			"The widget is enabled, but visitors are told the assistant is unavailable until the agent runs again.")
	}
	if w.Script == nil {
		diags.AddWarning("This console serves no widget script",
			"The widget is enabled, but this console image carries no widget build, so `embed_snippet` and the script attributes are empty.")
	}
}

// ── request and state mapping ──────────────────────────────────────────────

func widgetConfigRequest(ctx context.Context, m agentWidgetModel, diags *diag.Diagnostics) console.AiAgentWidgetConfig {
	body := console.AiAgentWidgetConfig{
		PrivacyUrl:            m.PrivacyURL.ValueString(),
		AllowAnyOrigin:        boolPtr(m.AllowAnyOrigin),
		Title:                 stringPtr(m.Title),
		Greeting:              stringPtr(m.Greeting),
		Colour:                stringPtr(m.Colour),
		Lang:                  stringPtr(m.Lang),
		SessionRetentionHours: int32PtrFromInt64(m.SessionRetentionHours),
		MaxQuestionLength:     int32PtrFromInt64(m.MaxQuestionLength),
		AllowedOrigins:        strSetPtr(ctx, m.AllowedOrigins, diags),
		Suggestions:           strListPtr(ctx, m.Suggestions, diags),
	}
	if body.AllowedOrigins != nil {
		slices.Sort(*body.AllowedOrigins)
	}
	return body
}

// widgetState maps the console's view onto the model. The console normalises
// what it stores (trimmed text, lowercase colour, origins as a browser sends
// them, parsed URLs), so a configured value that normalises to what the
// console holds is kept as written: neither the apply nor the next refresh
// then reports a difference that is only spelling.
func widgetState(ctx context.Context, prior agentWidgetModel, w *console.AiAgentWidgetResponse, baseURL string, diags *diag.Diagnostics) agentWidgetModel {
	out := prior
	cfg := w.Config
	if cfg == nil {
		cfg = &console.AiAgentWidgetConfig{}
	}
	out.AllowAnyOrigin = types.BoolValue(cfg.AllowAnyOrigin != nil && *cfg.AllowAnyOrigin)
	out.AllowedOrigins = keepEquivalentSet(ctx, prior.AllowedOrigins, derefStrings(cfg.AllowedOrigins), normalizeOrigin, diags)
	out.Suggestions = keepEquivalentList(ctx, prior.Suggestions, derefStrings(cfg.Suggestions), strings.TrimSpace, diags)
	out.Title = keepEquivalent(prior.Title, cfg.Title, strings.TrimSpace)
	out.Greeting = keepEquivalent(prior.Greeting, cfg.Greeting, strings.TrimSpace)
	out.Colour = keepEquivalent(prior.Colour, cfg.Colour, normalizeColour)
	out.Lang = keepEquivalent(prior.Lang, cfg.Lang, strings.TrimSpace)
	out.PrivacyURL = keepEquivalent(prior.PrivacyURL, &cfg.PrivacyUrl, normalizeURL)
	out.SessionRetentionHours = optInt64FromInt32(cfg.SessionRetentionHours)
	out.MaxQuestionLength = optInt64FromInt32(cfg.MaxQuestionLength)

	out.WidgetID = optNonEmpty(w.WidgetId)
	out.Phase = types.StringValue(string(w.Phase))
	out.EffectiveMaxQuestionLength = types.Int64Value(int64(w.EffectiveMaxQuestionLength))
	out.IneligibleReason = optNonEmpty(w.IneligibleReason)
	warnings, d := types.ListValueFrom(ctx, types.StringType, nonNilStrings(w.Warnings))
	diags.Append(d...)
	out.Warnings = warnings

	out.ScriptFloatingPath, out.ScriptPinnedPath, out.ScriptIntegrity = types.StringNull(), types.StringNull(), types.StringNull()
	out.EmbedSnippet = types.StringNull()
	if s := w.Script; s != nil {
		out.ScriptFloatingPath = types.StringValue(s.FloatingPath)
		out.ScriptPinnedPath = types.StringValue(s.PinnedPath)
		out.ScriptIntegrity = types.StringValue(s.Integrity)
		if w.WidgetId != nil && *w.WidgetId != "" {
			out.EmbedSnippet = types.StringValue(widgetSnippet(baseURL, s.FloatingPath, *w.WidgetId, cfg.Lang))
		}
	}
	return out
}

// widgetSnippet renders the tag a site pastes, the floating form: it follows
// the console's fixes and survives upgrades. The script derives the API origin
// from its own src, so the src must be the console's origin.
func widgetSnippet(baseURL, scriptPath, widgetID string, lang *string) string {
	origin := baseURL
	if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<script defer src="%s" data-agent="%s"`, html.EscapeString(origin+scriptPath), html.EscapeString(widgetID))
	if lang != nil && *lang != "" {
		fmt.Fprintf(&b, ` data-lang="%s"`, html.EscapeString(*lang))
	}
	b.WriteString(`></script>`)
	return b.String()
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// keepEquivalent returns prior when it normalises to what the API holds, else
// the API's value (null when absent or empty).
func keepEquivalent(prior types.String, api *string, norm func(string) string) types.String {
	held := ""
	if api != nil {
		held = *api
	}
	if !prior.IsNull() && !prior.IsUnknown() && norm(prior.ValueString()) == held {
		return prior
	}
	return nullIfEmptyString(held)
}

func keepEquivalentSet(ctx context.Context, prior types.Set, api []string, norm func(string) string, diags *diag.Diagnostics) types.Set {
	if !prior.IsNull() && !prior.IsUnknown() {
		values, d := setToStringSlice(ctx, prior)
		diags.Append(d...)
		if sameStrings(normalizeAll(values, norm, true), normalizeAll(api, norm, true)) {
			return prior
		}
	}
	return nullIfEmptySet(ctx, api, diags)
}

func keepEquivalentList(ctx context.Context, prior types.List, api []string, norm func(string) string, diags *diag.Diagnostics) types.List {
	if !prior.IsNull() && !prior.IsUnknown() {
		values, d := listToStringSlice(ctx, prior)
		diags.Append(d...)
		if slices.Equal(normalizeAll(values, norm, false), api) {
			return prior
		}
	}
	return nullIfEmptyList(ctx, api, diags)
}

// normalizeAll normalises each value and drops the empty ones; asSet also
// sorts and deduplicates.
func normalizeAll(values []string, norm func(string) string, asSet bool) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if n := norm(v); n != "" {
			out = append(out, n)
		}
	}
	if asSet {
		slices.Sort(out)
		out = slices.Compact(out)
	}
	return out
}

func sameStrings(a, b []string) bool {
	b = slices.Clone(b)
	slices.Sort(b)
	return slices.Equal(a, slices.Compact(b))
}

// normalizeOrigin folds an origin the way the console stores it: lowercase
// scheme and host, no default port, no trailing slash.
func normalizeOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// normalizeURL folds a URL the way the console's parser serialises it:
// lowercase scheme and host, no default port, `/` for an empty path.
func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && (u.Scheme != "https" || port != "443") {
		host += ":" + port
	}
	u.Host = host
	if u.Path == "" && u.RawPath == "" {
		u.Path = "/"
	}
	return u.String()
}

// normalizeColour folds `#ABC` and `#AABBCC` to `#aabbcc`.
func normalizeColour(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if len(v) == 4 && v[0] == '#' {
		return "#" + string([]byte{v[1], v[1], v[2], v[2], v[3], v[3]})
	}
	return v
}
