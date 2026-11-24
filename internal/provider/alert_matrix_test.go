// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

const teamsURL = "https://prod-42.westeurope.logic.azure.com:443/workflows/a/triggers/manual/paths/invoke?sig=supersecret"

// A Teams channel is a webhook like Slack: the URL is sent on create, never
// stored, kept by an update that does not send one and replaced by a rotation.
func TestAlertChannelTeamsLifecycle(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	plan := webhookChannelPlan(teamsURL)
	plan.Name, plan.Kind = types.StringValue("ops-teams"), types.StringValue("teams_webhook")

	created, d := l.create(plan)
	mustSucceed(t, d)
	post := f.calls("POST", `/alerting/channels`)[0].body
	if post["url"] != teamsURL || post["kind"] != "teams_webhook" {
		t.Errorf("create body = %v", post)
	}
	if _, ok := post["matrix"]; ok {
		t.Error("a Teams channel sent matrix settings")
	}
	if !created.URL.IsNull() || created.URLMasked.IsNull() || strings.Contains(created.URLMasked.ValueString(), "supersecret") {
		t.Errorf("created = %+v", created)
	}
	if !created.Matrix.IsNull() {
		t.Errorf("a Teams channel has matrix settings in state: %v", created.Matrix)
	}

	rename := created
	rename.Name = types.StringValue("ops-teams-2")
	renamed, d := l.update(rename, created)
	mustSucceed(t, d)
	if _, ok := f.calls("PUT", `/alerting/channels/[^/]+`)[0].body["url"]; ok {
		t.Error("an update without url in the config sent one")
	}
	if f.urls[created.ID.ValueString()] != teamsURL {
		t.Error("the stored URL was lost by an update that did not send one")
	}

	rotate := renamed
	rotate.URL = types.StringValue(teamsURL + "-rotated")
	rotate.URLWoVersion = types.StringValue("2")
	_, d = l.update(rotate, renamed)
	mustSucceed(t, d)
	if f.calls("PUT", `/alerting/channels/[^/]+`)[1].body["url"] != teamsURL+"-rotated" {
		t.Error("the rotation did not send the new URL")
	}

	noURL := renamed
	noURL.URLWoVersion = types.StringValue("3")
	_, d = l.update(noURL, renamed)
	mustFail(t, d, "`url` is required when `url_wo_version` changes")

	missing := plan
	missing.URL = types.StringNull()
	_, d = l.create(missing)
	mustFail(t, d, "`url` is required")
}

func matrixObject(t *testing.T, botID, room, roomID string) types.Object {
	t.Helper()
	id := types.StringUnknown()
	if roomID != "" {
		id = types.StringValue(roomID)
	}
	o, d := types.ObjectValueFrom(context.Background(), alertChannelMatrixAttrTypes(), alertChannelMatrixModel{
		BotID: types.StringValue(botID), Room: types.StringValue(room), RoomID: id,
	})
	mustSucceed(t, d)
	return o
}

func matrixOf(t *testing.T, m alertChannelModel) alertChannelMatrixModel {
	t.Helper()
	var out alertChannelMatrixModel
	mustSucceed(t, m.Matrix.As(context.Background(), &out, basetypes.ObjectAsOptions{}))
	return out
}

func matrixChannelPlan(t *testing.T, botID, room string) alertChannelModel {
	return alertChannelModel{
		ID: types.StringUnknown(), Name: types.StringValue("ops-tchap"), Kind: types.StringValue("matrix"),
		Recipients: types.SetNull(types.StringType), URL: types.StringNull(), URLWoVersion: types.StringNull(),
		URLMasked: types.StringUnknown(), Enabled: types.BoolValue(true), Matrix: matrixObject(t, botID, room, ""),
	}
}

// A Matrix channel configured with an alias keeps the alias in `room` — the
// API stores the resolved id — so a read after apply shows no diff, while
// `room_id` carries the id. Import reads the alias back from room_label.
func TestAlertChannelMatrixAliasHasNoDiff(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	bot := uuid.NewString()

	created, d := l.create(matrixChannelPlan(t, bot, "#ops:x.org"))
	mustSucceed(t, d)
	post := f.calls("POST", `/alerting/channels`)[0].body
	if m, _ := post["matrix"].(map[string]any); m["bot_id"] != bot || m["room"] != "#ops:x.org" {
		t.Errorf("create body matrix = %v", post["matrix"])
	}
	for _, k := range []string{"url", "recipients"} {
		if _, ok := post[k]; ok {
			t.Errorf("a Matrix channel sent %s", k)
		}
	}
	m := matrixOf(t, created)
	if m.Room.ValueString() != "#ops:x.org" || m.RoomID.ValueString() != fakeRoomID("#ops:x.org") || m.BotID.ValueString() != bot {
		t.Errorf("created matrix = %+v", m)
	}

	read, removed, d := l.read(created)
	mustSucceed(t, d)
	if removed || !read.Matrix.Equal(created.Matrix) {
		t.Errorf("read matrix = %v, want %v (no diff)", read.Matrix, created.Matrix)
	}

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if !imported.Matrix.Equal(created.Matrix) {
		t.Errorf("imported matrix = %v, want the alias from room_label", imported.Matrix)
	}

	// Repointed outside Terraform: the read follows the API.
	f.channels[created.ID.ValueString()].Matrix = &console.MatrixChannelDto{BotId: uuid.MustParse(bot), RoomId: "!other:x.org"}
	drifted, _, d := l.read(created)
	mustSucceed(t, d)
	if got := matrixOf(t, drifted); got.Room.ValueString() != "!other:x.org" || got.RoomID.ValueString() != "!other:x.org" {
		t.Errorf("drifted matrix = %+v, want the stored room id", got)
	}
}

// An update that keeps the bot and the room sends the room id already
// resolved, not the alias: a rename must not re-resolve the alias through the
// bot (it fails while the bot is down, and an alias repointed since would move
// the channel under a plan that kept the old room_id). A changed room is sent
// as configured, so the platform resolves it again.
func TestAlertChannelMatrixUpdateKeepsTheResolvedRoom(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	bot := uuid.NewString()

	created, d := l.create(matrixChannelPlan(t, bot, "#ops:x.org"))
	mustSucceed(t, d)

	rename := matrixChannelPlan(t, bot, "#ops:x.org")
	rename.Name = types.StringValue("renamed")
	renamed, d := l.update(rename, created)
	mustSucceed(t, d)
	put := f.calls("PUT", `/alerting/channels/[^/]+`)[0].body
	if m, _ := put["matrix"].(map[string]any); m["room"] != fakeRoomID("#ops:x.org") {
		t.Errorf("rename sent room %v, want the resolved id %q", m["room"], fakeRoomID("#ops:x.org"))
	}
	if got := matrixOf(t, renamed); got.Room.ValueString() != "#ops:x.org" {
		t.Errorf("renamed room = %q, want the configured alias kept", got.Room.ValueString())
	}

	moved, d := l.update(matrixChannelPlan(t, bot, "#infra:x.org"), renamed)
	mustSucceed(t, d)
	put = f.calls("PUT", `/alerting/channels/[^/]+`)[1].body
	if m, _ := put["matrix"].(map[string]any); m["room"] != "#infra:x.org" {
		t.Errorf("a changed room sent %v, want the configured alias", m["room"])
	}
	if got := matrixOf(t, moved); got.RoomID.ValueString() != fakeRoomID("#infra:x.org") {
		t.Errorf("moved room_id = %q", got.RoomID.ValueString())
	}
}

// A room configured by id round-trips as the id; the bot id keeps the case it
// was configured with.
func TestAlertChannelMatrixRoomID(t *testing.T) {
	_, pd := newFakeAlerting(t)
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	bot := strings.ToUpper(uuid.NewString())

	created, d := l.create(matrixChannelPlan(t, bot, "!abc:x.org"))
	mustSucceed(t, d)
	m := matrixOf(t, created)
	if m.Room.ValueString() != "!abc:x.org" || m.RoomID.ValueString() != "!abc:x.org" || m.BotID.ValueString() != bot {
		t.Errorf("created matrix = %+v", m)
	}
	read, _, d := l.read(created)
	mustSucceed(t, d)
	if !read.Matrix.Equal(created.Matrix) {
		t.Errorf("read matrix = %v, want %v", read.Matrix, created.Matrix)
	}
}

// room_id stays known in the plan while the bot and room are unchanged, and
// goes unknown when either changes.
func TestAlertChannelMatrixRoomIDPlan(t *testing.T) {
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{})
	bot := uuid.NewString()
	state := matrixChannelPlan(t, bot, "#ops:x.org")
	state.ID = types.StringValue(uuid.NewString())
	state.URLMasked = types.StringNull()
	state.Matrix = matrixObject(t, bot, "#ops:x.org", "!resolved-ops:x.org")

	run := func(room string) types.String {
		plan := state
		plan.Name = types.StringValue("renamed")
		plan.Matrix = matrixObject(t, bot, room, "")
		req := planmodifier.StringRequest{
			Path:       path.Root("matrix").AtName("room_id"),
			Plan:       l.plan(plan),
			State:      l.state(state),
			PlanValue:  types.StringUnknown(),
			StateValue: types.StringValue("!resolved-ops:x.org"),
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		matrixRoomIDUnlessRoomChanged{}.PlanModifyString(context.Background(), req, resp)
		mustSucceed(t, resp.Diagnostics)
		return resp.PlanValue
	}
	if got := run("#ops:x.org"); got.ValueString() != "!resolved-ops:x.org" {
		t.Errorf("unchanged room: room_id plan = %v, want the state value", got)
	}
	if got := run("#other:x.org"); !got.IsUnknown() {
		t.Errorf("changed room: room_id plan = %v, want unknown", got)
	}
}

func TestAlertChannelMatrixValidateConfig(t *testing.T) {
	r := &alertChannelResource{}
	s := resourceSchema(t, r).Schema
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	matrix := func(bot, room string) tftypes.Value {
		return objectValue(t, s, path.Root("matrix"), map[string]tftypes.Value{"bot_id": str(bot), "room": str(room)})
	}
	bot := uuid.NewString()
	for _, tc := range []struct {
		name    string
		attrs   map[string]tftypes.Value
		wantErr string
	}{
		{"matrix with an alias", map[string]tftypes.Value{"kind": str("matrix"), "matrix": matrix(bot, "#ops:x.org")}, ""},
		{"matrix with a room id", map[string]tftypes.Value{"kind": str("matrix"), "matrix": matrix(bot, "!abc:x.org")}, ""},
		{"matrix without settings", map[string]tftypes.Value{"kind": str("matrix")}, "Missing matrix settings"},
		{"matrix with a url", map[string]tftypes.Value{"kind": str("matrix"), "matrix": matrix(bot, "!a:x.org"), "url": str(teamsURL)}, "URL on a Matrix channel"},
		{"matrix with recipients", map[string]tftypes.Value{"kind": str("matrix"), "matrix": matrix(bot, "!a:x.org"),
			"recipients": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{str("a@b.io")})}, "Recipients on a Matrix channel"},
		{"matrix with a bad bot id", map[string]tftypes.Value{"kind": str("matrix"), "matrix": matrix("bot", "!a:x.org")}, "Invalid bot id"},
		{"matrix with a bad room", map[string]tftypes.Value{"kind": str("matrix"), "matrix": matrix(bot, "ops")}, "Invalid Matrix room"},
		{"teams with matrix settings", map[string]tftypes.Value{"kind": str("teams_webhook"), "url": str(teamsURL), "matrix": matrix(bot, "!a:x.org")}, "Matrix settings on a non-Matrix channel"},
		{"email with matrix settings", map[string]tftypes.Value{"kind": str("email"), "recipients": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String},
			[]tftypes.Value{str("a@b.io")}), "matrix": matrix(bot, "!a:x.org")}, "Matrix settings on a non-Matrix channel"},
		{"teams with a url", map[string]tftypes.Value{"kind": str("teams_webhook"), "url": str(teamsURL)}, ""},
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

func TestAlertChannelMatrixDataSource(t *testing.T) {
	f, pd := newFakeAlerting(t)
	bot := uuid.New()
	f.bots[bot.String()] = &console.MatrixBotDto{Id: bot, Name: "tchap-bot"}
	l := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	_, d := l.create(matrixChannelPlan(t, bot.String(), "#ops:x.org"))
	mustSucceed(t, d)

	got, d := readDataSource(t, &alertChannelDataSource{p: pd}, alertChannelDataSourceModel{
		Name: types.StringValue("ops-tchap"), ID: types.StringNull(), Kind: types.StringNull(),
		Recipients: types.SetNull(types.StringType), URLMasked: types.StringNull(), Enabled: types.BoolNull(),
		Matrix: types.ObjectNull(alertChannelDataSourceMatrixAttrTypes()),
	})
	mustSucceed(t, d)
	var m alertChannelDataSourceMatrixModel
	mustSucceed(t, got.Matrix.As(context.Background(), &m, basetypes.ObjectAsOptions{}))
	if got.Kind.ValueString() != "matrix" || m.BotName.ValueString() != "tchap-bot" ||
		m.RoomID.ValueString() != fakeRoomID("#ops:x.org") || m.RoomLabel.ValueString() != "#ops:x.org" {
		t.Errorf("data source = %+v, matrix %+v", got, m)
	}
}

func matrixBotPlan() alertMatrixBotModel {
	return alertMatrixBotModel{
		ID: types.StringUnknown(), Name: types.StringValue("tchap-alerts"), Preset: types.StringValue("tchap"),
		HomeserverURL: types.StringNull(), UserID: types.StringValue("@bot-alerts:agent.dinum.tchap.gouv.fr"),
		PasswordSecretID: types.StringValue(uuid.NewString()), RecoveryKeySecretID: types.StringValue(uuid.NewString()),
		AllowUnencryptedRooms: types.BoolValue(false), Phase: types.StringUnknown(), DeviceID: types.StringUnknown(),
		Message: types.StringUnknown(), ReloginRequired: types.BoolUnknown(),
	}
}

// A bot is created, updated, imported and destroyed; destroying it while a
// channel names it surfaces the API's 409 as an error and leaves it in place.
func TestAlertMatrixBotLifecycle(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertMatrixBotModel](t, &alertMatrixBotResource{p: pd})

	plan := matrixBotPlan()
	created, d := l.create(plan)
	mustSucceed(t, d)
	body := f.calls("POST", `/alerting/matrix-bots`)[0].body
	if body["preset"] != "tchap" || body["password_secret_id"] != plan.PasswordSecretID.ValueString() ||
		body["user_id"] != plan.UserID.ValueString() || body["allow_unencrypted_rooms"] != false {
		t.Errorf("create body = %v", body)
	}
	if _, ok := body["homeserver_url"]; ok {
		t.Error("a Tchap bot sent a homeserver_url")
	}
	if created.ID.IsUnknown() || created.Phase.ValueString() != "pending" || !created.DeviceID.IsNull() || created.ReloginRequired.ValueBool() {
		t.Errorf("created = %+v", created)
	}

	f.bots[created.ID.ValueString()].Status = console.MatrixBotStatusDto{Phase: console.MatrixBotPhaseNeedsLogin,
		DeviceId: ptr("DEV1"), Message: ptr("re-login needed"), ReloginRequired: true}
	read, removed, d := l.read(created)
	mustSucceed(t, d)
	if removed || read.Phase.ValueString() != "needs_login" || read.DeviceID.ValueString() != "DEV1" || !read.ReloginRequired.ValueBool() {
		t.Errorf("read = %+v", read)
	}

	next := read
	next.Name = types.StringValue("tchap-alerts-2")
	next.AllowUnencryptedRooms = types.BoolValue(true)
	updated, d := l.update(next, read)
	mustSucceed(t, d)
	if updated.Name.ValueString() != "tchap-alerts-2" || !updated.AllowUnencryptedRooms.ValueBool() {
		t.Errorf("updated = %+v", updated)
	}

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if !imported.ID.Equal(updated.ID) || !imported.PasswordSecretID.Equal(updated.PasswordSecretID) ||
		!imported.RecoveryKeySecretID.Equal(updated.RecoveryKeySecretID) || !imported.UserID.Equal(updated.UserID) ||
		!imported.HomeserverURL.Equal(updated.HomeserverURL) || !imported.AllowUnencryptedRooms.Equal(updated.AllowUnencryptedRooms) {
		t.Errorf("imported = %+v, want %+v", imported, updated)
	}

	channels := newLifecycle[alertChannelModel](t, &alertChannelResource{p: pd})
	ch, d := channels.create(matrixChannelPlan(t, created.ID.ValueString(), "!abc:x.org"))
	mustSucceed(t, d)
	d = l.destroy(updated)
	mustFail(t, d, "Matrix bot still in use")
	if _, ok := f.bots[created.ID.ValueString()]; !ok {
		t.Error("a bot still named by a channel was deleted")
	}

	mustSucceed(t, channels.destroy(ch))
	mustSucceed(t, l.destroy(updated))
	if _, removed, _ := l.read(updated); !removed {
		t.Error("a deleted bot stayed in state")
	}
}

func TestAlertMatrixBotGenericHomeserver(t *testing.T) {
	f, pd := newFakeAlerting(t)
	l := newLifecycle[alertMatrixBotModel](t, &alertMatrixBotResource{p: pd})
	plan := matrixBotPlan()
	plan.Preset, plan.HomeserverURL = types.StringValue("generic"), types.StringValue("https://matrix.example.org")
	plan.UserID = types.StringValue("@bot:example.org")
	created, d := l.create(plan)
	mustSucceed(t, d)
	if f.calls("POST", `/alerting/matrix-bots`)[0].body["homeserver_url"] != "https://matrix.example.org" ||
		created.HomeserverURL.ValueString() != "https://matrix.example.org" {
		t.Errorf("created = %+v", created)
	}
}

func TestAlertMatrixBotValidateConfig(t *testing.T) {
	r := &alertMatrixBotResource{}
	s := resourceSchema(t, r).Schema
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	base := func(extra map[string]tftypes.Value) map[string]tftypes.Value {
		m := map[string]tftypes.Value{
			"name": str("b"), "preset": str("tchap"), "user_id": str("@bot:agent.tchap.gouv.fr"),
			"password_secret_id": str(uuid.NewString()), "recovery_key_secret_id": str(uuid.NewString()),
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct {
		name    string
		attrs   map[string]tftypes.Value
		wantErr string
	}{
		{"tchap", base(nil), ""},
		{"tchap with a homeserver", base(map[string]tftypes.Value{"homeserver_url": str("https://m.example.org")}), "Homeserver URL with the Tchap preset"},
		{"generic without a homeserver", base(map[string]tftypes.Value{"preset": str("generic")}), "Missing homeserver URL"},
		{"generic over http", base(map[string]tftypes.Value{"preset": str("generic"), "homeserver_url": str("http://m.example.org")}), "Missing homeserver URL"},
		{"generic over https", base(map[string]tftypes.Value{"preset": str("generic"), "homeserver_url": str("https://m.example.org")}), ""},
		{"bad mxid", base(map[string]tftypes.Value{"user_id": str("bot")}), "Invalid MXID"},
		{"bad secret id", base(map[string]tftypes.Value{"password_secret_id": str("my-secret")}), "Invalid secret id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &fwresource.ValidateConfigResponse{}
			r.ValidateConfig(context.Background(), fwresource.ValidateConfigRequest{Config: nullConfig(t, s, tc.attrs)}, resp)
			if tc.wantErr == "" {
				mustSucceed(t, resp.Diagnostics)
				return
			}
			mustFail(t, resp.Diagnostics, tc.wantErr)
		})
	}
	if d := validateString(t, s, nullConfig(t, s, nil), path.Root("preset"), types.StringValue("element")); !d.HasError() {
		t.Error("an unknown preset must be refused")
	}
}

func TestAlertMatrixBotDataSource(t *testing.T) {
	_, pd := newFakeAlerting(t)
	created, d := newLifecycle[alertMatrixBotModel](t, &alertMatrixBotResource{p: pd}).create(matrixBotPlan())
	mustSucceed(t, d)
	cfg := alertMatrixBotDataSourceModel{Name: types.StringValue("tchap-alerts"), ID: types.StringNull(),
		Preset: types.StringNull(), HomeserverURL: types.StringNull(), UserID: types.StringNull(),
		AllowUnencryptedRooms: types.BoolNull(), Phase: types.StringNull(), DeviceID: types.StringNull(),
		Message: types.StringNull(), ReloginRequired: types.BoolNull()}
	got, d := readDataSource(t, &alertMatrixBotDataSource{p: pd}, cfg)
	mustSucceed(t, d)
	if !got.ID.Equal(created.ID) || got.Preset.ValueString() != "tchap" || got.Phase.ValueString() != "pending" {
		t.Errorf("data source = %+v", got)
	}
	cfg.Name = types.StringValue("missing")
	_, d = readDataSource(t, &alertMatrixBotDataSource{p: pd}, cfg)
	mustFail(t, d, "not found")
}

func TestAlertMatrixBotFeatureForbidden(t *testing.T) {
	f, pd := newFakeAlerting(t)
	f.forbidden = "Your organization does not have access to the alerting feature"
	_, d := newLifecycle[alertMatrixBotModel](t, &alertMatrixBotResource{p: pd}).create(matrixBotPlan())
	mustFail(t, d, "alerting_enabled")
}
