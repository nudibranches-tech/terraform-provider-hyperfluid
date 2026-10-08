// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ── config builders ───────────────────────────────────────────────────────

// containerAppObjectType is the tftypes shape of a hyperfluid_container_app
// configuration, plus the shapes of its two nested blocks.
func containerAppObjectType(t *testing.T) (app, source, git tftypes.Object) {
	t.Helper()
	s := resourceSchema(t, NewContainerAppResource()).Schema
	app, ok := s.Type().TerraformType(t.Context()).(tftypes.Object)
	if !ok {
		t.Fatal("schema does not carry an object type")
	}
	source, ok = app.AttributeTypes["image_source"].(tftypes.Object)
	if !ok {
		t.Fatal("image_source is not an object")
	}
	git, ok = source.AttributeTypes["git"].(tftypes.Object)
	if !ok {
		t.Fatal("image_source.git is not an object")
	}
	return app, source, git
}

func fillNulls(ty tftypes.Object, members map[string]tftypes.Value) tftypes.Value {
	out := make(map[string]tftypes.Value, len(ty.AttributeTypes))
	for name, at := range ty.AttributeTypes {
		out[name] = tftypes.NewValue(at, nil)
	}
	for name, v := range members {
		out[name] = v
	}
	return tftypes.NewValue(ty, out)
}

// gitFields are the string members of a `git` block; "interval" etc. stay null
// unless named.
func gitBlockValue(t *testing.T, fields map[string]string, alert *bool) tftypes.Value {
	t.Helper()
	_, _, gitType := containerAppObjectType(t)
	members := map[string]tftypes.Value{}
	for k, v := range fields {
		members[k] = tftypes.NewValue(tftypes.String, v)
	}
	if alert != nil {
		members["alert_on_sync_failure"] = tftypes.NewValue(tftypes.Bool, *alert)
	}
	return fillNulls(gitType, members)
}

// appConfig builds a full configuration: env and name set, everything else null
// unless overridden.
func appConfig(t *testing.T, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	appType, _, _ := containerAppObjectType(t)
	members := map[string]tftypes.Value{
		"env":  tftypes.NewValue(tftypes.String, "11111111-1111-1111-1111-111111111111"),
		"name": tftypes.NewValue(tftypes.String, "orders-api"),
	}
	for k, v := range overrides {
		members[k] = v
	}
	return fillNulls(appType, members)
}

func sourceBlockValue(t *testing.T, git tftypes.Value) tftypes.Value {
	t.Helper()
	_, srcType, _ := containerAppObjectType(t)
	return fillNulls(srcType, map[string]tftypes.Value{"git": git})
}

func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

var minimalGit = map[string]string{"provider": "github", "repository": "acme/orders-api"}

// validateContainerApp runs the real provider server's ValidateResourceConfig,
// so the schema validators, the block rules and ValidateConfig all apply.
func validateContainerApp(t *testing.T, cfg tftypes.Value) []*tfprotov6.Diagnostic {
	t.Helper()
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("provider server: %v", err)
	}
	dv, err := tfprotov6.NewDynamicValue(cfg.Type(), cfg)
	if err != nil {
		t.Fatalf("dynamic value: %v", err)
	}
	resp, err := srv.ValidateResourceConfig(t.Context(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: "hyperfluid_container_app",
		Config:   &dv,
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	var errs []*tfprotov6.Diagnostic
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			errs = append(errs, d)
		}
	}
	return errs
}

func diagText(ds []*tfprotov6.Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString(d.Summary + ": " + d.Detail + "\n")
	}
	return b.String()
}

// ── validation ────────────────────────────────────────────────────────────

func TestContainerAppImageSourceValidation(t *testing.T) {
	literal := map[string]tftypes.Value{"image_repository": str("nginx"), "image_tag": str("1")}
	with := func(base map[string]tftypes.Value, extra map[string]tftypes.Value) map[string]tftypes.Value {
		out := map[string]tftypes.Value{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	git := func(extra map[string]string) tftypes.Value {
		fields := map[string]string{}
		for k, v := range minimalGit {
			fields[k] = v
		}
		for k, v := range extra {
			fields[k] = v
		}
		return sourceBlockValue(t, gitBlockValue(t, fields, nil))
	}

	cases := []struct {
		name    string
		cfg     map[string]tftypes.Value
		wantErr string // substring of the diagnostics; empty means valid
	}{
		{"a literal image", literal, ""},
		{"a Git source", map[string]tftypes.Value{"image_source": git(nil)}, ""},
		{"a Git source following a branch", map[string]tftypes.Value{"image_source": git(map[string]string{"branch": "main"})}, ""},
		{"a Git source following tags", map[string]tftypes.Value{"image_source": git(map[string]string{"tag_pattern": `v?(?<version>\d+\.\d+\.\d+)`})}, ""},
		{"a Git source with every field", map[string]tftypes.Value{"image_source": git(map[string]string{
			"base_url": "https://ghe.example.com", "credential": "production/git/acme", "branch": "main",
			"path": "deploy/hyperfluid.toml", "container": "orders", "interval": "15m",
		})}, ""},
		{"no image at all", nil, "image_repository is required"},
		{"a repository without a tag", map[string]tftypes.Value{"image_repository": str("nginx")}, "image_tag is required"},
		{"a source next to a literal image", with(literal, map[string]tftypes.Value{"image_source": git(nil)}), "image_source"},
		{"a git block without a repository", map[string]tftypes.Value{"image_source": sourceBlockValue(t, gitBlockValue(t, map[string]string{"provider": "github"}, nil))}, "Missing repository"},
		{"an empty image_source block", map[string]tftypes.Value{"image_source": sourceBlockValue(t, tftypes.NewValue(mustGitType(t), nil))}, "git"},
		{"a branch and a tag pattern", map[string]tftypes.Value{"image_source": git(map[string]string{"branch": "main", "tag_pattern": "v(?<version>1)"})}, "tag_pattern"},
		{"an unknown provider", map[string]tftypes.Value{"image_source": git(map[string]string{"provider": "bitbucket"})}, "provider"},
		{"an interval below the minimum", map[string]tftypes.Value{"image_source": git(map[string]string{"interval": "1m"})}, "minimum"},
		{"an interval that is not a duration", map[string]tftypes.Value{"image_source": git(map[string]string{"interval": "often"})}, "not a duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateContainerApp(t, appConfig(t, tc.cfg))
			got := diagText(errs)
			switch {
			case tc.wantErr == "" && len(errs) > 0:
				t.Errorf("unexpected errors:\n%s", got)
			case tc.wantErr != "" && !strings.Contains(got, tc.wantErr):
				t.Errorf("errors = %q, want one mentioning %q", got, tc.wantErr)
			}
		})
	}
}

func mustGitType(t *testing.T) tftypes.Object {
	t.Helper()
	_, _, g := containerAppObjectType(t)
	return g
}

// ── schema ────────────────────────────────────────────────────────────────

func TestContainerAppImageSourceSchema(t *testing.T) {
	s := resourceSchema(t, NewContainerAppResource()).Schema

	for _, name := range []string{"image_repository", "image_tag"} {
		a, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok || a.Required || !a.Optional || a.Computed {
			t.Errorf("%s must be optional only, so a Git source can replace it: %+v", name, a)
		}
	}
	for _, name := range []string{"resolved_image", "resolved_ref", "revision", "last_synced_at", "sync_error"} {
		a, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok || !a.Computed || a.Optional || a.Required {
			t.Errorf("%s must be computed only: %+v", name, a)
		}
	}

	// No optional field of the git block may be Computed or carry a default: a
	// plan must never write a platform default into the app.
	git := s.Blocks["image_source"].(schema.SingleNestedBlock).Blocks["git"].(schema.SingleNestedBlock)
	for name, a := range git.Attributes {
		switch a := a.(type) {
		case schema.StringAttribute:
			if a.Computed || a.Default != nil {
				t.Errorf("git.%s must not be computed or defaulted", name)
			}
		case schema.BoolAttribute:
			if a.Computed || a.Default != nil {
				t.Errorf("git.%s must not be computed or defaulted", name)
			}
		}
	}

	d := dataSourceSchema(t, NewContainerAppDataSource())
	for _, name := range []string{"image_source", "resolved_image", "resolved_ref", "revision", "last_synced_at", "sync_error"} {
		if _, ok := d.Schema.Attributes[name]; !ok {
			t.Errorf("data source has no %q attribute", name)
		}
	}
}

// ── plan modifier ─────────────────────────────────────────────────────────

func TestGitStatusIsOnlyPlannedUnknownWithASource(t *testing.T) {
	ctx := t.Context()
	s := resourceSchema(t, NewContainerAppResource()).Schema
	appType, _, _ := containerAppObjectType(t)

	cases := []struct {
		name        string
		source      tftypes.Value
		wantUnknown bool
	}{
		{"no source", tftypes.NewValue(mustSourceType(t), nil), false},
		{"a source", sourceBlockValue(t, gitBlockValue(t, minimalGit, nil)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := appConfigWithType(appType, tc.source)
			req := planmodifier.StringRequest{
				Path:      path.Root("resolved_image"),
				Config:    tfsdk.Config{Schema: s, Raw: cfg},
				PlanValue: types.StringUnknown(),
			}
			resp := &planmodifier.StringResponse{PlanValue: types.StringUnknown()}
			gitStatusModifier{}.PlanModifyString(ctx, req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("diagnostics: %v", resp.Diagnostics)
			}
			if got := resp.PlanValue.IsUnknown(); got != tc.wantUnknown {
				t.Errorf("planned unknown = %v, want %v (plan = %s)", got, tc.wantUnknown, resp.PlanValue)
			}
		})
	}
}

func mustSourceType(t *testing.T) tftypes.Object {
	t.Helper()
	_, s, _ := containerAppObjectType(t)
	return s
}

func appConfigWithType(appType tftypes.Object, source tftypes.Value) tftypes.Value {
	return fillNulls(appType, map[string]tftypes.Value{"image_source": source})
}

// ── request body ──────────────────────────────────────────────────────────

func objectOf(t *testing.T, m gitSourceModel) types.Object {
	t.Helper()
	git, d := types.ObjectValueFrom(t.Context(), gitSourceAttrTypes(), m)
	if d.HasError() {
		t.Fatalf("git object: %v", d.Errors())
	}
	obj, d := types.ObjectValue(imageSourceAttrTypes(), map[string]attr.Value{"git": git})
	if d.HasError() {
		t.Fatalf("source object: %v", d.Errors())
	}
	return obj
}

func nullGitModel() gitSourceModel {
	return gitSourceModel{
		Provider: types.StringNull(), Repository: types.StringNull(), BaseURL: types.StringNull(),
		Credential: types.StringNull(), Branch: types.StringNull(), TagPattern: types.StringNull(),
		Path: types.StringNull(), Container: types.StringNull(), Interval: types.StringNull(),
		AlertOnSyncFailure: types.BoolNull(),
	}
}

func marshalSource(t *testing.T, src console.ImageSource) map[string]map[string]any {
	t.Helper()
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

// TestImageSourceBodyOmitsPlatformDefaults is the regression that stops a
// default being frozen into the app: a field the user left out must not reach
// the request at all, so the platform keeps resolving it itself.
func TestImageSourceBodyOmitsPlatformDefaults(t *testing.T) {
	m := nullGitModel()
	m.Provider = types.StringValue("github")
	m.Repository = types.StringValue("acme/orders-api")

	src, err := buildImageSource(m)
	if err != nil {
		t.Fatalf("buildImageSource: %v", err)
	}
	git := marshalSource(t, src)["git"]
	if len(git) != 2 || git["provider"] != "github" || git["repository"] != "acme/orders-api" {
		t.Errorf("git = %v, want only provider and repository", git)
	}

	// And the API's answer for such an app goes back to a block with every
	// optional field null — including the alert, which the API always reports.
	alert := true
	obj, err := gitSourceToObject(&src, &alert, &m)
	if err != nil {
		t.Fatalf("gitSourceToObject: %v", err)
	}
	got, d := gitSourceFromObject(t.Context(), obj)
	if d.HasError() || got == nil {
		t.Fatalf("read back: %v / %v", got, d)
	}
	if !got.Provider.Equal(m.Provider) || !got.Repository.Equal(m.Repository) {
		t.Errorf("required fields changed on the way back: %+v", got)
	}
	got.Provider, got.Repository = types.StringNull(), types.StringNull()
	if *got != nullGitModel() {
		t.Errorf("optional fields were materialised: %+v", got)
	}
}

func TestImageSourceBodyCarriesWhatTheUserSet(t *testing.T) {
	m := nullGitModel()
	m.Provider = types.StringValue("gitlab")
	m.Repository = types.StringValue("acme/platform/orders")
	m.BaseURL = types.StringValue("https://gitlab.example.com")
	m.Credential = types.StringValue("production/git/acme")
	m.Branch = types.StringValue("release")
	m.Path = types.StringValue("deploy/hyperfluid.toml")
	m.Container = types.StringValue("orders")
	m.Interval = types.StringValue("15m")
	m.AlertOnSyncFailure = types.BoolValue(false)

	src, err := buildImageSource(m)
	if err != nil {
		t.Fatalf("buildImageSource: %v", err)
	}
	git := marshalSource(t, src)["git"]
	want := map[string]any{
		"provider": "gitlab", "repository": "acme/platform/orders", "baseUrl": "https://gitlab.example.com",
		"path": "deploy/hyperfluid.toml", "container": "orders", "intervalSeconds": float64(900),
		"credential": map[string]any{"name": "production/git/acme", "type": "controlplane"},
		"ref":        map[string]any{"type": "Branch", "name": "release"},
	}
	for k, w := range want {
		if gotJSON, _ := json.Marshal(git[k]); string(gotJSON) != mustJSON(t, w) {
			t.Errorf("%s = %s, want %s", k, gotJSON, mustJSON(t, w))
		}
	}
	if len(git) != len(want) {
		t.Errorf("git = %v, want exactly %d keys", git, len(want))
	}

	// Round trip, with an explicit `false` alert kept.
	alert := false
	obj, err := gitSourceToObject(&src, &alert, &m)
	if err != nil {
		t.Fatalf("gitSourceToObject: %v", err)
	}
	got, _ := gitSourceFromObject(t.Context(), obj)
	if got == nil || *got != m {
		t.Errorf("round trip = %+v, want %+v", got, m)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestImageSourceTagPatternRoundTrip(t *testing.T) {
	m := nullGitModel()
	m.Provider = types.StringValue("forgejo")
	m.Repository = types.StringValue("acme/orders")
	m.TagPattern = types.StringValue(`web-(?<version>1\.\d+\.\d+)`)

	src, err := buildImageSource(m)
	if err != nil {
		t.Fatalf("buildImageSource: %v", err)
	}
	ref, _ := marshalSource(t, src)["git"]["ref"].(map[string]any)
	if ref["type"] != "Tag" || ref["pattern"] != m.TagPattern.ValueString() || len(ref) != 2 {
		t.Errorf("ref = %v", ref)
	}
	obj, err := gitSourceToObject(&src, nil, &m)
	if err != nil {
		t.Fatalf("gitSourceToObject: %v", err)
	}
	got, _ := gitSourceFromObject(t.Context(), obj)
	if got == nil || !got.TagPattern.Equal(m.TagPattern) || !got.Branch.IsNull() {
		t.Errorf("round trip = %+v", got)
	}
}

// TestImageSourceKeepsTheUsersSpelling: an equivalent value the platform stores
// differently must not read back as a change the user never made.
func TestImageSourceKeepsTheUsersSpelling(t *testing.T) {
	m := nullGitModel()
	m.Provider = types.StringValue("github")
	m.Repository = types.StringValue("acme/orders")
	m.BaseURL = types.StringValue("https://GHE.example.com/")
	m.Interval = types.StringValue("900s")

	secs := int32(900)
	base := "https://ghe.example.com"
	src := apiSource(t, console.GitImageSource{
		Provider: console.GitProviderGithub, Repository: "acme/orders", BaseUrl: &base, IntervalSeconds: &secs,
	})
	obj, err := gitSourceToObject(&src, nil, &m)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := gitSourceFromObject(t.Context(), obj)
	if got == nil || !got.BaseURL.Equal(m.BaseURL) || !got.Interval.Equal(m.Interval) {
		t.Errorf("spelling lost: %+v", got)
	}

	// With nothing to preserve (an import), the canonical spelling is used.
	obj, err = gitSourceToObject(&src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = gitSourceFromObject(t.Context(), obj)
	if got == nil || got.Interval.ValueString() != "15m" {
		t.Errorf("interval = %v, want 15m", got)
	}
}

func apiSource(t *testing.T, git console.GitImageSource) console.ImageSource {
	t.Helper()
	var src console.ImageSource
	if err := src.FromImageSource0(console.ImageSource0{Git: git}); err != nil {
		t.Fatal(err)
	}
	return src
}

// TestImageSourceAlertDefaultIsNotMaterialised: the API always reports the
// alert; only a deliberate setting may land in state.
func TestImageSourceAlertDefaultIsNotMaterialised(t *testing.T) {
	src := apiSource(t, console.GitImageSource{Provider: console.GitProviderGithub, Repository: "acme/orders"})
	yes, no := true, false

	explicit := nullGitModel()
	explicit.AlertOnSyncFailure = types.BoolValue(true)

	for _, tc := range []struct {
		name  string
		api   *bool
		prior *gitSourceModel
		want  types.Bool
	}{
		{"default, never set", &yes, ptr(nullGitModel()), types.BoolNull()},
		{"default, on import", &yes, nil, types.BoolNull()},
		{"disabled", &no, ptr(nullGitModel()), types.BoolValue(false)},
		{"disabled, on import", &no, nil, types.BoolValue(false)},
		{"explicitly true", &yes, &explicit, types.BoolValue(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj, err := gitSourceToObject(&src, tc.api, tc.prior)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := gitSourceFromObject(t.Context(), obj)
			if got == nil || !got.AlertOnSyncFailure.Equal(tc.want) {
				t.Errorf("alert = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── interval ──────────────────────────────────────────────────────────────

func TestInterval(t *testing.T) {
	for in, want := range map[string]int32{"5m": 300, "300s": 300, "15m": 900, "1h": 3600, "24h": 86400, "90m": 5400} {
		got, err := parseInterval(in)
		if err != nil || got != want {
			t.Errorf("parseInterval(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "4m59s", "1m", "soon", "5", "5m0.5s", "-1h", "999999999h"} {
		if _, err := parseInterval(in); err == nil {
			t.Errorf("parseInterval(%q) succeeded, want an error", in)
		}
	}
	for secs, want := range map[int32]string{300: "5m", 900: "15m", 3600: "1h", 86400: "24h", 5400: "1h30m", 301: "5m1s"} {
		if got := formatInterval(secs); got != want {
			t.Errorf("formatInterval(%d) = %q, want %q", secs, got, want)
		}
	}
	for _, d := range []string{"5m", "15m", "1h", "24h", "1h30m"} {
		secs, err := parseInterval(d)
		if err != nil || formatInterval(secs) != d {
			t.Errorf("%q does not survive a round trip: %d, %v, %q", d, secs, err, formatInterval(secs))
		}
	}
}

func TestRenderResolvedImage(t *testing.T) {
	tag, digest := "1.4.2", "sha256:4f2a"
	for _, tc := range []struct {
		in   console.ResolvedImage
		want string
	}{
		{console.ResolvedImage{Repository: "ghcr.io/acme/orders", Tag: &tag}, "ghcr.io/acme/orders:1.4.2"},
		{console.ResolvedImage{Repository: "ghcr.io/acme/orders", Tag: &tag, Digest: &digest}, "ghcr.io/acme/orders:1.4.2@sha256:4f2a"},
		{console.ResolvedImage{Repository: "ghcr.io/acme/orders", Digest: &digest}, "ghcr.io/acme/orders@sha256:4f2a"},
		{console.ResolvedImage{Repository: "ghcr.io/acme/orders"}, "ghcr.io/acme/orders"},
	} {
		if got := renderResolvedImage(tc.in); got != tc.want {
			t.Errorf("renderResolvedImage = %q, want %q", got, tc.want)
		}
	}
}

func TestGitStatusFrom(t *testing.T) {
	if got := gitStatusFrom(nil); got != nullGitStatus() {
		t.Errorf("no status must be all null: %+v", got)
	}
	empty := ""
	ref, rev, at, msg := "main", "4f9c2e1", "2026-10-08T10:00:00Z", "401 Unauthorized"
	got := gitStatusFrom(&console.ImageSourceStatus{
		Resolved: &console.ResolvedImage{Repository: "r", Tag: ptr("1")}, ResolvedRef: &ref, Revision: &rev,
		LastSyncedAt: &at, Error: &msg,
	})
	want := gitStatus{
		ResolvedImage: types.StringValue("r:1"), ResolvedRef: types.StringValue("main"),
		Revision: types.StringValue("4f9c2e1"), LastSyncedAt: types.StringValue(at), SyncError: types.StringValue(msg),
	}
	if got != want {
		t.Errorf("gitStatusFrom = %+v, want %+v", got, want)
	}
	if got := gitStatusFrom(&console.ImageSourceStatus{Revision: &empty}); got.Revision != types.StringNull() {
		t.Errorf("an empty string must read as null: %+v", got)
	}
}
