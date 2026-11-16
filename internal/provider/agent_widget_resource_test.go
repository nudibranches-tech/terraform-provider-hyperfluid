// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeWidget serves the widget of one agent the way the console does: the PUT
// normalises what it stores (lowercase colour, origins as a browser sends
// them, trimmed text), the id is minted on the first enable and kept through
// disable and enable again, and rotate mints a new one.
type fakeWidget struct {
	*fakeAPI
	agent     uuid.UUID
	cfg       *console.AiAgentWidgetConfig
	id        string
	enabled   bool
	rotations int
	build     string
	stopped   bool
	noScript  bool
	agentGone bool
	refuse    string // a 400 message the next PUT answers with
	baseURL   string
}

func newFakeWidget(t *testing.T) (*fakeWidget, *agentWidgetResource) {
	f, pd := newFakeAPI(t)
	fw := &fakeWidget{fakeAPI: f, agent: uuid.New(), build: "0123456789abcdef", baseURL: pd.API.BaseURL()}
	fw.routes(t)
	return fw, &agentWidgetResource{p: pd}
}

func (fw *fakeWidget) routes(t *testing.T) {
	notFound := func() (int, any) { return 404, map[string]string{"message": "AI agent not found"} }
	fw.on("GET", `/ai-agents/[^/]+/widget`, func(string, []byte) (int, any) {
		if fw.agentGone {
			return notFound()
		}
		return 200, fw.response()
	})
	fw.on("PUT", `/ai-agents/[^/]+/widget`, func(_ string, raw []byte) (int, any) {
		if fw.agentGone {
			return notFound()
		}
		if fw.refuse != "" {
			return 400, map[string]string{"message": fw.refuse}
		}
		req := decodeInto[console.AiAgentWidgetConfig](t, raw)
		if req.Colour != nil {
			c := strings.ToLower(*req.Colour)
			req.Colour = &c
		}
		if req.Title != nil {
			s := strings.TrimSpace(*req.Title)
			req.Title = &s
		}
		if req.AllowedOrigins != nil {
			var out []string
			for _, o := range *req.AllowedOrigins {
				out = append(out, normalizeOrigin(o))
			}
			req.AllowedOrigins = &out
		}
		if req.AllowAnyOrigin != nil && !*req.AllowAnyOrigin {
			req.AllowAnyOrigin = nil // the console stores false as absent
		}
		fw.cfg = &req
		fw.enabled = true
		if fw.id == "" {
			fw.id = "wa_first"
		}
		return 200, fw.response()
	})
	fw.on("POST", `/ai-agents/[^/]+/widget/rotate`, func(string, []byte) (int, any) {
		if fw.cfg == nil {
			return 404, map[string]string{"message": "this agent has no widget"}
		}
		fw.rotations++
		fw.id = fmt.Sprintf("wa_rotated%d", fw.rotations)
		return 200, fw.response()
	})
	fw.on("POST", `/ai-agents/[^/]+/widget/disable`, func(string, []byte) (int, any) {
		if fw.cfg == nil || fw.agentGone {
			return 404, map[string]string{"message": "this agent has no widget"}
		}
		fw.enabled = false
		return 200, fw.response()
	})
}

func (fw *fakeWidget) response() console.AiAgentWidgetResponse {
	out := console.AiAgentWidgetResponse{
		Enabled: fw.enabled, Phase: console.AiAgentWidgetPhaseDtoNotConfigured, Config: fw.cfg,
		EffectiveMaxQuestionLength: 800,
		Warnings:                   []string{"anonymous visitors can get answers from everything this agent's service account can read"},
	}
	if fw.cfg != nil {
		id := fw.id
		out.WidgetId = &id
		switch {
		case !fw.enabled:
			out.Phase = console.AiAgentWidgetPhaseDtoDisabled
		case fw.stopped:
			out.Phase = console.AiAgentWidgetPhaseDtoAgentStopped
		default:
			out.Phase = console.AiAgentWidgetPhaseDtoActive
		}
		if fw.cfg.MaxQuestionLength != nil {
			out.EffectiveMaxQuestionLength = *fw.cfg.MaxQuestionLength
		}
	}
	if !fw.noScript {
		out.Script = &console.AiAgentWidgetScript{
			FloatingPath: "/widget/v1/agent.js", PinnedPath: "/widget/v1.4.2/" + fw.build + "/agent.js",
			Version: "1.4.2", Build: fw.build, Integrity: "sha384-" + fw.build,
		}
	}
	return out
}

func widgetPlan(fw *fakeWidget) agentWidgetModel {
	return agentWidgetModel{
		ID: types.StringUnknown(), AgentID: types.StringValue(fw.agent.String()),
		AllowedOrigins: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("https://www.Example.org/")}),
		AllowAnyOrigin: types.BoolValue(false),
		Title:          types.StringValue("Data assistant"), Greeting: types.StringValue("Ask me about our open data."),
		Colour:      types.StringValue("#0B6E69"),
		Suggestions: types.ListValueMust(types.StringType, []attr.Value{types.StringValue("Which datasets?"), types.StringValue("How fresh?")}),
		Lang:        types.StringValue("fr"), PrivacyURL: types.StringValue("https://www.example.org/privacy"),
		SessionRetentionHours: types.Int64Null(), MaxQuestionLength: types.Int64Value(500),
		RotationTrigger: types.StringNull(),

		WidgetID: types.StringUnknown(), Phase: types.StringUnknown(),
		ScriptFloatingPath: types.StringUnknown(), ScriptPinnedPath: types.StringUnknown(), ScriptIntegrity: types.StringUnknown(),
		EmbedSnippet: types.StringUnknown(), EffectiveMaxQuestionLength: types.Int64Unknown(),
		Warnings: types.ListUnknown(types.StringType), IneligibleReason: types.StringUnknown(),
	}
}

func TestAgentWidgetSchema(t *testing.T) {
	assertSchemaImplementation(t, NewAgentWidgetResource())
}

// Create enables the widget with exactly the configured fields, keeps what
// the console only re-spelled as written, and builds the snippet from the
// provider's console URL.
func TestAgentWidgetCreate(t *testing.T) {
	fw, r := newFakeWidget(t)
	l := newLifecycle[agentWidgetModel](t, r)
	got, d := l.create(widgetPlan(fw))
	mustSucceed(t, d)

	puts := fw.calls("PUT", `/ai-agents/[^/]+/widget`)
	if len(puts) != 1 {
		t.Fatalf("PUT calls = %d, want 1", len(puts))
	}
	body := puts[0].body
	if _, ok := body["session_retention_hours"]; ok {
		t.Errorf("PUT carries session_retention_hours, which the config does not set: %v", body)
	}
	if body["privacy_url"] != "https://www.example.org/privacy" || body["lang"] != "fr" || body["max_question_length"] != float64(500) {
		t.Errorf("PUT body = %v", body)
	}
	if !strings.HasSuffix(puts[0].path, "/organizations/"+fw.org.String()+"/ai-agents/"+fw.agent.String()+"/widget") {
		t.Errorf("PUT path = %s", puts[0].path)
	}

	if got.ID.ValueString() != fw.agent.String() || got.WidgetID.ValueString() != "wa_first" || got.Phase.ValueString() != "Active" {
		t.Errorf("state = %+v", got)
	}
	// Re-spelled by the console, kept as configured: no inconsistent result.
	if got.Colour.ValueString() != "#0B6E69" || !got.AllowedOrigins.Equal(widgetPlan(fw).AllowedOrigins) {
		t.Errorf("colour = %s, origins = %s: the configured spelling was not kept", got.Colour, got.AllowedOrigins)
	}
	want := fmt.Sprintf(`<script defer src="%s/widget/v1/agent.js" data-agent="wa_first" data-lang="fr"></script>`, r.p.API.BaseURL())
	if got.EmbedSnippet.ValueString() != want {
		t.Errorf("snippet = %s\nwant      %s", got.EmbedSnippet.ValueString(), want)
	}
	if got.ScriptPinnedPath.ValueString() != "/widget/v1.4.2/0123456789abcdef/agent.js" || got.ScriptIntegrity.ValueString() != "sha384-0123456789abcdef" {
		t.Errorf("script = %s %s", got.ScriptPinnedPath, got.ScriptIntegrity)
	}
	if got.EffectiveMaxQuestionLength.ValueInt64() != 500 || len(got.Warnings.Elements()) != 1 {
		t.Errorf("effective = %s, warnings = %s", got.EffectiveMaxQuestionLength, got.Warnings)
	}
	if len(d.Warnings()) == 0 || !strings.Contains(d.Warnings()[0].Detail(), "service account can read") {
		t.Errorf("the apply did not report the platform's warnings: %v", d.Warnings())
	}
	if n := len(fw.calls("POST", `/widget/rotate`)); n != 0 {
		t.Errorf("create rotated the widget %d time(s)", n)
	}
}

// An update sends the whole configuration again, so a field removed from the
// configuration is cleared; it never rotates on its own.
func TestAgentWidgetUpdate(t *testing.T) {
	fw, r := newFakeWidget(t)
	l := newLifecycle[agentWidgetModel](t, r)
	created, d := l.create(widgetPlan(fw))
	mustSucceed(t, d)

	next := created
	next.Title = types.StringValue("Open data assistant")
	next.Greeting = types.StringNull()
	next.Lang = types.StringNull()
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	puts := fw.calls("PUT", `/ai-agents/[^/]+/widget`)
	if len(puts) != 2 {
		t.Fatalf("PUT calls = %d, want 2", len(puts))
	}
	body := puts[1].body
	if _, ok := body["greeting"]; ok {
		t.Errorf("PUT still carries greeting, which was removed: %v", body)
	}
	if body["title"] != "Open data assistant" {
		t.Errorf("PUT body = %v", body)
	}
	if n := len(fw.calls("POST", `/widget/rotate`)); n != 0 {
		t.Errorf("an update rotated the widget %d time(s)", n)
	}
	if updated.WidgetID.ValueString() != "wa_first" || !updated.Greeting.IsNull() || strings.Contains(updated.EmbedSnippet.ValueString(), "data-lang") {
		t.Errorf("updated = %+v", updated)
	}

	// Nothing but computed values differing: nothing is sent.
	again, d := l.update(updated, updated)
	mustSucceed(t, d)
	if n := len(fw.calls("PUT", `/ai-agents/[^/]+/widget`)); n != 2 {
		t.Errorf("an unchanged configuration was sent again (%d PUTs)", n)
	}
	if !again.WidgetID.Equal(updated.WidgetID) {
		t.Errorf("widget id moved: %s", again.WidgetID)
	}
}

// Rotation is opt-in: a new trigger value rotates once, the same value does not
// again, and removing the trigger does not rotate.
func TestAgentWidgetRotate(t *testing.T) {
	fw, r := newFakeWidget(t)
	l := newLifecycle[agentWidgetModel](t, r)
	plan := widgetPlan(fw)
	plan.RotationTrigger = types.StringValue("2026-11")
	created, d := l.create(plan)
	mustSucceed(t, d)
	if fw.rotations != 0 || created.RotationTrigger.ValueString() != "2026-11" {
		t.Fatalf("a trigger set at create rotated (%d) or was lost (%s)", fw.rotations, created.RotationTrigger)
	}

	next := created
	next.RotationTrigger = types.StringValue("2026-12")
	rotated, d := l.update(next, created)
	mustSucceed(t, d)
	if fw.rotations != 1 || rotated.WidgetID.ValueString() != "wa_rotated1" {
		t.Fatalf("rotations = %d, widget id = %s", fw.rotations, rotated.WidgetID)
	}
	if !strings.Contains(rotated.EmbedSnippet.ValueString(), `data-agent="wa_rotated1"`) {
		t.Errorf("the snippet still names the old id: %s", rotated.EmbedSnippet)
	}
	if n := len(fw.calls("PUT", `/ai-agents/[^/]+/widget`)); n != 1 {
		t.Errorf("a rotation alone re-sent the configuration (%d PUTs)", n)
	}

	removed := rotated
	removed.RotationTrigger = types.StringNull()
	after, d := l.update(removed, rotated)
	mustSucceed(t, d)
	if fw.rotations != 1 || after.WidgetID.ValueString() != "wa_rotated1" {
		t.Errorf("removing the trigger rotated: rotations = %d, id = %s", fw.rotations, after.WidgetID)
	}
}

// Destroy disables the widget; an agent already gone has nothing to disable.
func TestAgentWidgetDisable(t *testing.T) {
	fw, r := newFakeWidget(t)
	l := newLifecycle[agentWidgetModel](t, r)
	created, d := l.create(widgetPlan(fw))
	mustSucceed(t, d)
	mustSucceed(t, l.destroy(created))
	if fw.enabled || len(fw.calls("POST", `/widget/disable`)) != 1 {
		t.Errorf("destroy did not disable the widget (enabled = %v)", fw.enabled)
	}
	if n := len(fw.calls("DELETE", `.*`)); n != 0 {
		t.Errorf("destroy deleted something (%d DELETEs): it must only disable the widget", n)
	}

	fw.agentGone = true
	mustSucceed(t, l.destroy(created))
}

// A widget disabled outside Terraform, or whose agent is gone, leaves state,
// so the next apply enables it again.
func TestAgentWidgetDrift(t *testing.T) {
	fw, r := newFakeWidget(t)
	l := newLifecycle[agentWidgetModel](t, r)
	created, d := l.create(widgetPlan(fw))
	mustSucceed(t, d)

	fw.build = "fedcba9876543210" // a console upgrade
	refreshed, removed, d := l.read(created)
	mustSucceed(t, d)
	if removed || refreshed.ScriptPinnedPath.ValueString() != "/widget/v1.4.2/fedcba9876543210/agent.js" {
		t.Errorf("the pinned path was not refreshed: %s", refreshed.ScriptPinnedPath)
	}
	if !refreshed.EmbedSnippet.Equal(created.EmbedSnippet) || !refreshed.Colour.Equal(created.Colour) {
		t.Errorf("a refresh moved the snippet or the configured colour: %s %s", refreshed.EmbedSnippet, refreshed.Colour)
	}

	fw.enabled = false
	if _, removed, d = l.read(created); d.HasError() || !removed {
		t.Errorf("a widget disabled outside Terraform stayed in state (%v)", d)
	}
	fw.enabled, fw.agentGone = true, true
	if _, removed, d = l.read(created); d.HasError() || !removed {
		t.Errorf("a widget whose agent is gone stayed in state (%v)", d)
	}
}

// Import takes the agent id and recovers the configuration as the console
// holds it; a disabled widget cannot be imported.
func TestAgentWidgetImport(t *testing.T) {
	fw, r := newFakeWidget(t)
	l := newLifecycle[agentWidgetModel](t, r)
	_, d := l.create(widgetPlan(fw))
	mustSucceed(t, d)

	imported, d := l.importAndRead(fw.agent.String())
	mustSucceed(t, d)
	if imported.AgentID.ValueString() != fw.agent.String() || imported.WidgetID.ValueString() != "wa_first" ||
		imported.Colour.ValueString() != "#0b6e69" || imported.PrivacyURL.ValueString() != "https://www.example.org/privacy" ||
		len(imported.Suggestions.Elements()) != 2 || !imported.RotationTrigger.IsNull() || imported.AllowAnyOrigin.ValueBool() {
		t.Errorf("imported = %+v", imported)
	}

	_, d = l.importAndRead("not-a-uuid")
	mustFail(t, d, "agent id")

	fw.enabled = false
	resp := l.readImported(fw.agent.String())
	if !resp {
		t.Error("a disabled widget was imported")
	}
}

// readImported reports whether the Read that follows an import dropped the
// resource, which Terraform reports as "cannot import non-existent remote object".
func (l *lifecycle[M]) readImported(id string) bool {
	l.t.Helper()
	resp := &fwresource.ImportStateResponse{State: tfsdk.State{Schema: l.s, Raw: l.null()}}
	ir, ok := l.r.(fwresource.ResourceWithImportState)
	if !ok {
		l.t.Fatal("the resource does not implement ImportState")
	}
	ir.ImportState(l.t.Context(), fwresource.ImportStateRequest{ID: id}, resp)
	mustSucceed(l.t, resp.Diagnostics)
	_, removed, d := l.readState(resp.State)
	mustSucceed(l.t, d)
	return removed
}

// The platform's refusals reach the user in its own words.
func TestAgentWidgetRefused(t *testing.T) {
	cases := map[string]string{
		"write rights":   "this agent has a public web widget, so its service account must be viewer-only; this one has write rights (editor)",
		"capability":     "a capability cannot have a public widget: enable it on its router",
		"privacy notice": "privacyUrl is required: the widget processes visitors' questions and IP addresses, and the visitor must be able to read your privacy notice",
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			fw, r := newFakeWidget(t)
			fw.refuse = msg
			l := newLifecycle[agentWidgetModel](t, r)
			_, d := l.create(widgetPlan(fw))
			mustFail(t, d, msg)
			if s := d.Errors()[0].Summary(); s != "The platform refused the agent widget" {
				t.Errorf("summary = %q", s)
			}
			if strings.Contains(d.Errors()[0].Detail(), "-> 400") {
				t.Errorf("the detail shows the raw response: %s", d.Errors()[0].Detail())
			}
		})
	}
}

// The organization without the AI agents feature is told which feature.
func TestAgentWidgetFeatureDisabled(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("PUT", `/ai-agents/[^/]+/widget`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": "Your organization does not have access to the AI agents feature"}
	})
	l := newLifecycle[agentWidgetModel](t, &agentWidgetResource{p: pd})
	_, d := l.create(widgetPlan(&fakeWidget{fakeAPI: f, agent: uuid.New()}))
	mustFail(t, d, "sapience_agent_enabled")
}

// An agent that is not running, or a console without a widget build, is said
// on the apply.
func TestAgentWidgetApplyWarnings(t *testing.T) {
	fw, r := newFakeWidget(t)
	fw.stopped, fw.noScript = true, true
	l := newLifecycle[agentWidgetModel](t, r)
	got, d := l.create(widgetPlan(fw))
	mustSucceed(t, d)
	var summaries []string
	for _, w := range d.Warnings() {
		summaries = append(summaries, w.Summary())
	}
	joined := strings.Join(summaries, " | ")
	if !strings.Contains(joined, "not running") || !strings.Contains(joined, "no widget script") {
		t.Errorf("warnings = %s", joined)
	}
	if got.Phase.ValueString() != "AgentStopped" || !got.EmbedSnippet.IsNull() || !got.ScriptPinnedPath.IsNull() {
		t.Errorf("state = %+v", got)
	}
}

func TestWidgetRotationRequested(t *testing.T) {
	s := types.StringValue
	cases := []struct {
		name        string
		plan, state types.String
		want        bool
	}{
		{"unset", types.StringNull(), types.StringNull(), false},
		{"first value", s("a"), types.StringNull(), true},
		{"same value", s("a"), s("a"), false},
		{"new value", s("b"), s("a"), true},
		{"removed", types.StringNull(), s("a"), false},
		{"unknown", types.StringUnknown(), s("a"), true},
	}
	for _, tc := range cases {
		if got := triggerRequested(tc.plan, tc.state); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWidgetNormalizers(t *testing.T) {
	for in, want := range map[string]string{
		"https://WWW.Example.org/":  "https://www.example.org",
		"https://example.org:443":   "https://example.org",
		"https://example.org:8443/": "https://example.org:8443",
		"http://localhost:5173":     "http://localhost:5173",
		" https://example.org ":     "https://example.org",
	} {
		if got := normalizeOrigin(in); got != want {
			t.Errorf("normalizeOrigin(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"https://Example.org":           "https://example.org/",
		"https://example.org/privacy":   "https://example.org/privacy",
		"https://example.org:443/a?b=c": "https://example.org/a?b=c",
	} {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := normalizeColour("#0Af"); got != "#00aaff" {
		t.Errorf("normalizeColour = %q", got)
	}
}

func TestWidgetSnippet(t *testing.T) {
	lang := "en"
	got := widgetSnippet("https://console.example.org/", "/widget/v1/agent.js", `wa_"x`, &lang)
	want := `<script defer src="https://console.example.org/widget/v1/agent.js" data-agent="wa_&#34;x" data-lang="en"></script>`
	if got != want {
		t.Errorf("snippet = %s\nwant      %s", got, want)
	}
	if got := widgetSnippet("https://console.example.org", "/widget/v1/agent.js", "wa_1", nil); strings.Contains(got, "data-lang") {
		t.Errorf("snippet without lang = %s", got)
	}
}
