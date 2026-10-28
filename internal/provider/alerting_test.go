// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeAlerting serves the alerting API from memory: channels keep their full
// webhook URL privately and return it masked, rules are replaced whole, and
// thresholds fall back to the platform defaults once their override is reset.
type fakeAlerting struct {
	*fakeAPI
	channels   map[string]*console.NotificationChannelDto
	urls       map[string]string
	rules      map[string]*console.AlertRuleDto
	thresholds map[string]*console.SignalThresholdsDto
	forbidden  string // when set, every call answers 403 with this message
}

var alertThresholdDefaults = map[string][2]int32{"pvc_disk": {80, 90}, "bucket_quota": {80, 95}}

func newFakeAlerting(t *testing.T) (*fakeAlerting, *providerData) {
	api, pd := newFakeAPI(t)
	f := &fakeAlerting{
		fakeAPI: api, channels: map[string]*console.NotificationChannelDto{}, urls: map[string]string{},
		rules: map[string]*console.AlertRuleDto{}, thresholds: map[string]*console.SignalThresholdsDto{},
	}
	for signal := range alertThresholdDefaults {
		f.resetThreshold(signal)
	}
	deny := func() (int, any, bool) {
		if f.forbidden == "" {
			return 0, nil, false
		}
		return 403, map[string]string{"message": f.forbidden}, true
	}
	last := func(p string) string { return p[strings.LastIndex(p, "/")+1:] }
	masked := func(u string) *string {
		if u == "" {
			return nil
		}
		m := u[:min(len(u), 24)] + "…"
		return &m
	}

	f.on("GET", `/alerting/channels`, func(string, []byte) (int, any) {
		if code, v, ok := deny(); ok {
			return code, v
		}
		out := []console.NotificationChannelDto{}
		for _, c := range f.channels {
			out = append(out, *c)
		}
		return 200, out
	})
	writeChannel := func(id string, body console.ChannelBody) *console.NotificationChannelDto {
		c := &console.NotificationChannelDto{Id: uuid.MustParse(id), Name: body.Name, Kind: body.Kind,
			Recipients: []string{}, Enabled: body.Enabled == nil || *body.Enabled, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if body.Recipients != nil {
			c.Recipients = *body.Recipients
		}
		if body.Url != nil {
			f.urls[id] = *body.Url
		}
		c.UrlMasked = masked(f.urls[id])
		f.channels[id] = c
		return c
	}
	f.on("POST", `/alerting/channels`, func(_ string, raw []byte) (int, any) {
		if code, v, ok := deny(); ok {
			return code, v
		}
		return 200, writeChannel(uuid.NewString(), decodeInto[console.ChannelBody](t, raw))
	})
	f.on("PUT", `/alerting/channels/[^/]+`, func(p string, raw []byte) (int, any) {
		if _, ok := f.channels[last(p)]; !ok {
			return 404, map[string]string{"message": "no such channel"}
		}
		return 200, writeChannel(last(p), decodeInto[console.ChannelBody](t, raw))
	})
	f.on("DELETE", `/alerting/channels/[^/]+`, func(p string, _ []byte) (int, any) {
		if code, v, ok := deny(); ok {
			return code, v
		}
		delete(f.channels, last(p))
		return 204, nil
	})

	f.on("GET", `/alerting/rules`, func(string, []byte) (int, any) {
		if code, v, ok := deny(); ok {
			return code, v
		}
		out := []console.AlertRuleDto{}
		for _, r := range f.rules {
			out = append(out, *r)
		}
		return 200, out
	})
	writeRule := func(id string, b console.AlertRuleBody) *console.AlertRuleDto {
		r := &console.AlertRuleDto{Id: uuid.MustParse(id), OrganizationId: f.org, ResourceKind: b.ResourceKind,
			ResourceRef: b.ResourceRef, ResourceName: b.ResourceName, PanelId: b.PanelId, Comparator: b.Comparator,
			WarningValue: b.WarningValue, CriticalValue: b.CriticalValue, ForSeconds: 300,
			Enabled: b.Enabled == nil || *b.Enabled, State: "ok", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if b.ForSeconds != nil {
			r.ForSeconds = *b.ForSeconds
		}
		f.rules[id] = r
		return r
	}
	f.on("POST", `/alerting/rules`, func(_ string, raw []byte) (int, any) {
		if code, v, ok := deny(); ok {
			return code, v
		}
		return 200, writeRule(uuid.NewString(), decodeInto[console.AlertRuleBody](t, raw))
	})
	f.on("PUT", `/alerting/rules/[^/]+`, func(p string, raw []byte) (int, any) {
		if _, ok := f.rules[last(p)]; !ok {
			return 404, map[string]string{"message": "no such rule"}
		}
		return 200, writeRule(last(p), decodeInto[console.AlertRuleBody](t, raw))
	})
	f.on("DELETE", `/alerting/rules/[^/]+`, func(p string, _ []byte) (int, any) {
		delete(f.rules, last(p))
		return 204, nil
	})

	f.on("GET", `/alerting/thresholds`, func(string, []byte) (int, any) {
		if code, v, ok := deny(); ok {
			return code, v
		}
		out := []console.SignalThresholdsDto{}
		for _, th := range f.thresholds {
			out = append(out, *th)
		}
		return 200, out
	})
	f.on("PUT", `/alerting/thresholds/[^/]+`, func(p string, raw []byte) (int, any) {
		if code, v, ok := deny(); ok {
			return code, v
		}
		b := decodeInto[console.SetThresholdsBody](t, raw)
		th := &console.SignalThresholdsDto{Signal: last(p), WarningPct: b.WarningPct, CriticalPct: b.CriticalPct,
			WarningIsOverride: true, CriticalIsOverride: true}
		f.thresholds[last(p)] = th
		return 200, th
	})
	f.on("DELETE", `/alerting/thresholds/[^/]+`, func(p string, _ []byte) (int, any) {
		return 200, f.resetThreshold(last(p))
	})
	return f, pd
}

func (f *fakeAlerting) resetThreshold(signal string) *console.SignalThresholdsDto {
	d := alertThresholdDefaults[signal]
	th := &console.SignalThresholdsDto{Signal: signal, WarningPct: d[0], CriticalPct: d[1]}
	f.thresholds[signal] = th
	return th
}

func stringSet(vals ...string) types.Set {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

func webhookChannelPlan(url string) alertChannelModel {
	return alertChannelModel{
		ID: types.StringUnknown(), Name: types.StringValue("ops-slack"), Kind: types.StringValue("slack_webhook"),
		Recipients: types.SetNull(types.StringType), URL: types.StringValue(url), URLWoVersion: types.StringValue("1"),
		URLMasked: types.StringUnknown(), Enabled: types.BoolValue(true),
	}
}

const slackURL = "https://hooks.slack.com/services/T000/B000/supersecret"

// A webhook channel sends its URL on create but never stores it; an update
// without one keeps the stored URL, and a version bump sends the new one.
func TestAlertChannelWebhookLifecycle(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})

	created, d := l.create(webhookChannelPlan(slackURL))
	mustSucceed(t, d)
	if got := f.calls("POST", `/alerting/channels`)[0].body["url"]; got != slackURL {
		t.Errorf("create url = %v, want the configured URL", got)
	}
	if !created.URL.IsNull() {
		t.Error("url reached state; it is write-only")
	}
	if created.URLMasked.IsNull() || strings.Contains(created.URLMasked.ValueString(), "supersecret") {
		t.Errorf("url_masked = %v, want the platform's masked URL", created.URLMasked)
	}
	if created.URLWoVersion.ValueString() != "1" || !created.Recipients.IsNull() {
		t.Errorf("state = %+v", created)
	}

	read, removed, d := l.read(created)
	mustSucceed(t, d)
	if removed || !read.Name.Equal(created.Name) || read.URLWoVersion.ValueString() != "1" {
		t.Errorf("read = %+v (removed %v)", read, removed)
	}

	rename := created
	rename.Name = types.StringValue("ops-alerts")
	rename.Enabled = types.BoolValue(false)
	renamed, d := l.update(rename, created)
	mustSucceed(t, d)
	put := f.calls("PUT", `/alerting/channels/[^/]+`)[0].body
	if _, ok := put["url"]; ok {
		t.Error("an update without url in the config sent one")
	}
	if renamed.Name.ValueString() != "ops-alerts" || renamed.Enabled.ValueBool() {
		t.Errorf("renamed = %+v", renamed)
	}
	if f.urls[created.ID.ValueString()] != slackURL {
		t.Error("the stored URL was lost by an update that did not send one")
	}

	rotate := renamed
	rotate.URL = types.StringValue("https://hooks.slack.com/services/T000/B000/rotated")
	rotate.URLWoVersion = types.StringValue("2")
	rotated, d := l.update(rotate, renamed)
	mustSucceed(t, d)
	if f.calls("PUT", `/alerting/channels/[^/]+`)[1].body["url"] != rotate.URL.ValueString() || rotated.URLWoVersion.ValueString() != "2" {
		t.Error("the rotation did not send the new URL")
	}

	noURL := rotated
	noURL.URLWoVersion = types.StringValue("3")
	_, d = l.update(noURL, rotated)
	mustFail(t, d, "`url` is required when `url_wo_version` changes")

	mustSucceed(t, l.destroy(rotated))
	if _, ok := f.channels[created.ID.ValueString()]; ok {
		t.Error("destroy left the channel in place")
	}
}

func TestAlertChannelWebhookCreateNeedsURL(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	plan := webhookChannelPlan("")
	plan.URL = types.StringNull()
	_, d := l.create(plan)
	mustFail(t, d, "`url` is required")
	if len(f.calls("POST", `/alerting/channels`)) != 0 {
		t.Error("a webhook channel without url reached the API")
	}
}

// An email channel round-trips its recipients; a channel deleted outside
// Terraform drops from state, and import reads one back by id.
func TestAlertChannelEmailDriftAndImport(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	created, d := l.create(alertChannelModel{
		ID: types.StringUnknown(), Name: types.StringValue("ops-mail"), Kind: types.StringValue("email"),
		Recipients: stringSet("ops@acme.io", "sre@acme.io"), URL: types.StringNull(), URLWoVersion: types.StringNull(),
		URLMasked: types.StringUnknown(), Enabled: types.BoolValue(true),
	})
	mustSucceed(t, d)
	if !created.Recipients.Equal(stringSet("sre@acme.io", "ops@acme.io")) || !created.URLMasked.IsNull() {
		t.Errorf("created = %+v", created)
	}

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if !imported.Name.Equal(created.Name) || !imported.Recipients.Equal(created.Recipients) {
		t.Errorf("imported = %+v", imported)
	}

	delete(f.channels, created.ID.ValueString())
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a deleted channel stayed in state")
	}
}

func TestAlertChannelDataSource(t *testing.T) {
	_, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	created, d := l.create(webhookChannelPlan(slackURL))
	mustSucceed(t, d)

	got, d := readDataSource(t, &alertChannelDataSource{p: pd}, alertChannelDataSourceModel{
		Name: types.StringValue("ops-slack"), ID: types.StringNull(), Kind: types.StringNull(),
		Recipients: types.SetNull(types.StringType), URLMasked: types.StringNull(), Enabled: types.BoolNull(),
	})
	mustSucceed(t, d)
	if !got.ID.Equal(created.ID) || got.Kind.ValueString() != "slack_webhook" || !got.URLMasked.Equal(created.URLMasked) {
		t.Errorf("data source = %+v", got)
	}

	_, d = readDataSource(t, &alertChannelDataSource{p: pd}, alertChannelDataSourceModel{
		Name: types.StringValue("missing"), ID: types.StringNull(), Kind: types.StringNull(),
		Recipients: types.SetNull(types.StringType), URLMasked: types.StringNull(), Enabled: types.BoolNull(),
	})
	mustFail(t, d, "not found")
}

func TestAlertChannelSchema(t *testing.T) {
	s := resourceSchema(t, &alertChannelResource{}).Schema
	url, ok := s.Attributes["url"].(schema.StringAttribute)
	if !ok || !url.WriteOnly || !url.Sensitive {
		t.Errorf("url must be write-only and sensitive: %+v", s.Attributes["url"])
	}
}

func TestAlertChannelValidateConfig(t *testing.T) {
	r := &alertChannelResource{}
	s := resourceSchema(t, r).Schema
	set := func(vals ...string) tftypes.Value {
		elems := make([]tftypes.Value, len(vals))
		for i, v := range vals {
			elems[i] = tftypes.NewValue(tftypes.String, v)
		}
		return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
	}
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	for _, tc := range []struct {
		name    string
		attrs   map[string]tftypes.Value
		wantErr string
	}{
		{"email with recipients", map[string]tftypes.Value{"kind": str("email"), "recipients": set("a@b.io")}, ""},
		{"email without recipients", map[string]tftypes.Value{"kind": str("email")}, "Missing recipients"},
		{"email with a url", map[string]tftypes.Value{"kind": str("email"), "recipients": set("a@b.io"), "url": str(slackURL)}, "URL on an email channel"},
		{"webhook with a url", map[string]tftypes.Value{"kind": str("slack_webhook"), "url": str(slackURL)}, ""},
		{"webhook with recipients", map[string]tftypes.Value{"kind": str("discord_webhook"), "recipients": set("a@b.io")}, "Recipients on a webhook channel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &fwresource.ValidateConfigResponse{}
			r.ValidateConfig(context.Background(), fwresource.ValidateConfigRequest{Config: nullConfig(t, s, tc.attrs)}, resp)
			if tc.wantErr == "" {
				mustSucceed(t, resp.Diagnostics)
				return
			}
			mustFail(t, resp.Diagnostics, tc.wantErr)
			for _, e := range resp.Diagnostics.Errors() {
				if strings.Contains(e.Detail(), "supersecret") {
					t.Error("a diagnostic quotes the webhook URL")
				}
			}
		})
	}
}

func alertRulePlan() alertRuleModel {
	return alertRuleModel{
		ID: types.StringUnknown(), ResourceKind: types.StringValue("trino"), ResourceRef: types.StringValue("my-lake"),
		ResourceName: types.StringUnknown(), PanelID: types.StringValue("trino_queued_queries"),
		Comparator: types.StringValue("gt"), WarningValue: types.Float64Value(10), CriticalValue: types.Float64Value(25),
		ForSeconds: types.Int64Value(600), Enabled: types.BoolValue(true),
	}
}

func TestAlertRuleLifecycle(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertRuleModel](t, &alertRuleResource{p: pd})

	created, d := l.create(alertRulePlan())
	mustSucceed(t, d)
	body := f.calls("POST", `/alerting/rules`)[0].body
	if body["resource_name"] != "my-lake" || body["for_seconds"] != float64(600) || body["critical_value"] != float64(25) {
		t.Errorf("create body = %v", body)
	}
	if created.ResourceName.ValueString() != "my-lake" || created.WarningValue.ValueFloat64() != 10 {
		t.Errorf("created = %+v", created)
	}

	next := created
	next.WarningValue = types.Float64Null()
	next.Comparator = types.StringValue("lt")
	next.Enabled = types.BoolValue(false)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if _, ok := f.calls("PUT", `/alerting/rules/[^/]+`)[0].body["warning_value"]; ok {
		t.Error("a removed warning_value was still sent")
	}
	if !updated.WarningValue.IsNull() || updated.Comparator.ValueString() != "lt" || updated.Enabled.ValueBool() {
		t.Errorf("updated = %+v", updated)
	}

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if !imported.ID.Equal(updated.ID) || !imported.CriticalValue.Equal(updated.CriticalValue) ||
		!imported.WarningValue.Equal(updated.WarningValue) || !imported.Comparator.Equal(updated.Comparator) ||
		!imported.ResourceName.Equal(updated.ResourceName) || !imported.ForSeconds.Equal(updated.ForSeconds) {
		t.Errorf("imported = %+v, want %+v", imported, updated)
	}

	mustSucceed(t, l.destroy(updated))
	if _, removed, _ := l.read(updated); !removed {
		t.Error("a deleted rule stayed in state")
	}
	if len(f.calls("DELETE", `/alerting/rules/[^/]+`)) != 1 {
		t.Error("destroy did not delete the rule")
	}
}

func TestAlertRuleValidateConfig(t *testing.T) {
	r := &alertRuleResource{}
	s := resourceSchema(t, r).Schema
	resp := &fwresource.ValidateConfigResponse{}
	r.ValidateConfig(context.Background(), fwresource.ValidateConfigRequest{Config: nullConfig(t, s, map[string]tftypes.Value{
		"resource_kind": tftypes.NewValue(tftypes.String, "scheduled_query"),
		"resource_name": tftypes.NewValue(tftypes.String, "nightly"),
	})}, resp)
	mustFail(t, resp.Diagnostics, "names a scheduled query's rule itself")
}

// The resource overrides a signal and destroy resets it, without ever writing
// the platform defaults as an override.
func TestAlertThresholdLifecycle(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertThresholdModel](t, &alertThresholdResource{p: pd})
	plan := alertThresholdModel{ID: types.StringUnknown(), Signal: types.StringValue("pvc_disk"),
		WarningPct: types.Int64Value(70), CriticalPct: types.Int64Value(85)}

	created, d := l.create(plan)
	mustSucceed(t, d)
	if created.ID.ValueString() != "pvc_disk" || created.WarningPct.ValueInt64() != 70 {
		t.Errorf("created = %+v", created)
	}

	next := created
	next.CriticalPct = types.Int64Value(95)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if updated.CriticalPct.ValueInt64() != 95 {
		t.Errorf("updated = %+v", updated)
	}

	imported, d := l.importAndRead("pvc_disk")
	mustSucceed(t, d)
	if imported != updated {
		t.Errorf("imported = %+v, want %+v", imported, updated)
	}

	mustSucceed(t, l.destroy(updated))
	if len(f.calls("DELETE", `/alerting/thresholds/pvc_disk`)) != 1 {
		t.Error("destroy did not reset the signal")
	}
	puts := f.calls("PUT", `/alerting/thresholds/[^/]+`)
	if len(puts) != 2 {
		t.Errorf("%d threshold writes, want the create and the update only", len(puts))
	}
	if th := f.thresholds["pvc_disk"]; th.WarningIsOverride || th.CriticalIsOverride {
		t.Error("the signal still carries an override after destroy")
	}
}

// A signal reset outside Terraform reads as removed, and so does importing a
// signal that has no override.
func TestAlertThresholdResetOutOfBand(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertThresholdModel](t, &alertThresholdResource{p: pd})
	created, d := l.create(alertThresholdModel{ID: types.StringUnknown(), Signal: types.StringValue("bucket_quota"),
		WarningPct: types.Int64Value(60), CriticalPct: types.Int64Value(80)})
	mustSucceed(t, d)

	f.resetThreshold("bucket_quota")
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a reset signal stayed in state")
	}
	_, removed, d = l.read(alertThresholdModel{ID: types.StringValue("pvc_disk"), Signal: types.StringValue("pvc_disk"),
		WarningPct: types.Int64Value(1), CriticalPct: types.Int64Value(2)})
	mustSucceed(t, d)
	if !removed {
		t.Error("a signal on its defaults read as an override")
	}
}

func TestAlertThresholdValidateConfig(t *testing.T) {
	r := &alertThresholdResource{}
	s := resourceSchema(t, r).Schema
	resp := &fwresource.ValidateConfigResponse{}
	r.ValidateConfig(context.Background(), fwresource.ValidateConfigRequest{Config: nullConfig(t, s, map[string]tftypes.Value{
		"signal":       tftypes.NewValue(tftypes.String, "pvc_disk"),
		"warning_pct":  tftypes.NewValue(tftypes.Number, 90),
		"critical_pct": tftypes.NewValue(tftypes.Number, 90),
	})}, resp)
	mustFail(t, resp.Diagnostics, "must be below critical_pct")
	if d := validateString(t, s, nullConfig(t, s, nil), path.Root("signal"), types.StringValue("cfs_throttling")); !d.HasError() {
		t.Error("cfs_throttling cannot be overridden and must be refused")
	}
	if !requiresReplaceString(t, s, path.Root("signal"), types.StringValue("pvc_disk"), types.StringValue("bucket_quota")) {
		t.Error("changing the signal must replace the override")
	}
}

// An organization without the alerting feature gets a diagnostic naming it;
// a missing permission keeps its own message.
func TestAlertingFeatureForbidden(t *testing.T) {
	f, pd := newFakeAlerting(t)
	f.forbidden = "Your organization does not have access to the alerting feature"

	_, d := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd}).create(webhookChannelPlan(slackURL))
	mustFail(t, d, "alerting_enabled")
	_, d = newLifecycle[alertRuleModel](t, &alertRuleResource{p: pd}).create(alertRulePlan())
	mustFail(t, d, "alerting_enabled")
	_, _, d = newLifecycle[alertThresholdModel](t, &alertThresholdResource{p: pd}).read(alertThresholdModel{
		ID: types.StringValue("pvc_disk"), Signal: types.StringValue("pvc_disk"),
		WarningPct: types.Int64Value(70), CriticalPct: types.Int64Value(85)})
	mustFail(t, d, "alerting_enabled")

	f.forbidden = "Missing permission 'alerting:manage'"
	_, d = newLifecycle[alertRuleModel](t, &alertRuleResource{p: pd}).create(alertRulePlan())
	mustFail(t, d, `missing permission "alerting:manage"`)
	for _, e := range d.Errors() {
		if strings.Contains(e.Detail(), "alerting_enabled") {
			t.Error("a missing permission was reported as the disabled feature")
		}
	}
}
