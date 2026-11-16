// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeChannels serves the chat channels of one agent the way the console does:
// a PUT replaces the block (trimmed, blank entries dropped) and lists the
// interface, a DELETE nulls it and drops the interface, the re-login answers
// 409 without a Matrix device, and the status is the operator's, changed by
// the test between reads.
type fakeChannels struct {
	*fakeAPI
	agent      uuid.UUID
	interfaces []string
	matrix     *console.AiAgentMatrixChannelConfig
	slack      *console.AiAgentSlackChannelConfig
	discord    *console.AiAgentDiscordChannelConfig
	limits     *console.AiAgentChannelLimitsDto
	phase      console.AiAgentChannelPhaseDto
	lastEvent  time.Time
	message    string
	device     string
	relogins   int
	pending    bool
	required   bool
	refuse     string
	agentGone  bool
	baseURL    string
	// lag is how many reads after a write still describe the configuration from
	// before it (settled false, phase Disabled), as an operator catching up.
	lag      int
	unsettle int
	// forbidSecrets makes the console refuse to bind the referenced secrets.
	forbidSecrets bool
}

func newFakeChannels(t *testing.T) (*fakeChannels, *providerData) {
	f, pd := newFakeAPI(t)
	fc := &fakeChannels{
		fakeAPI: f, agent: uuid.New(), interfaces: []string{"web"},
		phase: console.AiAgentChannelPhaseDtoConnecting, lastEvent: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC),
		baseURL: pd.API.BaseURL(),
	}
	fc.routes(t)
	return fc, pd
}

func trimList(l *[]string) *[]string {
	if l == nil {
		return &[]string{}
	}
	out := []string{}
	for _, v := range *l {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return &out
}

func trimText(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	return &v
}

// secretAccessDenied is the console's answer to a channel that references a
// secret the caller may not bind.
var secretAccessDenied = map[string]string{
	"message": "binding the secret in password_secret_id to this agent needs secret:access on it: the agent uses its value",
}

func (fc *fakeChannels) list(i string) {
	if !slices.Contains(fc.interfaces, i) {
		fc.interfaces = append(fc.interfaces, i)
	}
}

func (fc *fakeChannels) drop(i string) {
	fc.interfaces = slices.DeleteFunc(fc.interfaces, func(v string) bool { return v == i })
}

func (fc *fakeChannels) routes(t *testing.T) {
	gone := func() (int, any) { return 404, map[string]string{"message": "AI agent not found"} }
	answer := func(write func() (int, any)) func(string, []byte) (int, any) {
		return func(string, []byte) (int, any) {
			if fc.agentGone {
				return gone()
			}
			if code, body := write(); code != 0 {
				return code, body
			}
			return 200, fc.response()
		}
	}
	put := func(pattern string, store func(raw []byte)) {
		fc.on("PUT", pattern, func(_ string, raw []byte) (int, any) {
			return answer(func() (int, any) {
				if fc.refuse != "" {
					return 400, map[string]string{"message": fc.refuse}
				}
				if fc.forbidSecrets && pattern != `/ai-agents/[^/]+/channel-limits` {
					return 403, secretAccessDenied
				}
				store(raw)
				fc.unsettle = fc.lag
				return 0, nil
			})("", raw)
		})
	}
	fc.on("GET", `/ai-agents/[^/]+/channels`, answer(func() (int, any) {
		if fc.unsettle > 0 {
			fc.unsettle--
		}
		return 0, nil
	}))
	put(`/ai-agents/[^/]+/channels/matrix`, func(raw []byte) {
		c := decodeInto[console.AiAgentMatrixChannelConfig](t, raw)
		c.UserId, c.HomeserverUrl = trimText(c.UserId), trimText(c.HomeserverUrl)
		c.AllowedRooms, c.AllowedUsers, c.ExtraAuthHosts = trimList(c.AllowedRooms), trimList(c.AllowedUsers), trimList(c.ExtraAuthHosts)
		if c.AllowExternalInference != nil && !*c.AllowExternalInference {
			c.AllowExternalInference = nil // false is stored as absent
		}
		fc.matrix = &c
		fc.list("matrix")
	})
	put(`/ai-agents/[^/]+/channels/slack`, func(raw []byte) {
		c := decodeInto[console.AiAgentSlackChannelConfig](t, raw)
		c.TeamId, c.AllowedChannels, c.AllowedUsers = trimText(c.TeamId), trimList(c.AllowedChannels), trimList(c.AllowedUsers)
		fc.slack = &c
		fc.list("slack")
	})
	put(`/ai-agents/[^/]+/channels/discord`, func(raw []byte) {
		c := decodeInto[console.AiAgentDiscordChannelConfig](t, raw)
		c.AllowedGuilds, c.AllowedChannels, c.AllowedUsers = trimList(c.AllowedGuilds), trimList(c.AllowedChannels), trimList(c.AllowedUsers)
		fc.discord = &c
		fc.list("discord")
	})
	put(`/ai-agents/[^/]+/channel-limits`, func(raw []byte) {
		l := decodeInto[console.AiAgentChannelLimitsDto](t, raw)
		fc.limits = &l
	})
	fc.on("DELETE", `/ai-agents/[^/]+/channels/matrix`, answer(func() (int, any) { fc.matrix = nil; fc.drop("matrix"); return 0, nil }))
	fc.on("DELETE", `/ai-agents/[^/]+/channels/slack`, answer(func() (int, any) { fc.slack = nil; fc.drop("slack"); return 0, nil }))
	fc.on("DELETE", `/ai-agents/[^/]+/channels/discord`, answer(func() (int, any) { fc.discord = nil; fc.drop("discord"); return 0, nil }))
	fc.on("DELETE", `/ai-agents/[^/]+/channel-limits`, answer(func() (int, any) { fc.limits = nil; return 0, nil }))
	fc.on("POST", `/ai-agents/[^/]+/channels/matrix/relogin`, answer(func() (int, any) {
		if fc.matrix == nil {
			return 409, map[string]string{"message": "This agent has no Matrix channel to log in again"}
		}
		if fc.device == "" {
			return 409, map[string]string{"message": "The Matrix bot has not completed its first login yet"}
		}
		if fc.forbidSecrets {
			return 403, secretAccessDenied
		}
		if fc.pending || !fc.required {
			return 409, map[string]string{"message": "No re-login is required, or one is already pending"}
		}
		fc.relogins++
		fc.pending, fc.required = true, false
		return 0, nil
	}))
	fc.on("GET", `/ai-agents/[^/]+/channels/slack/manifest`, answer(func() (int, any) {
		return 200, console.AiAgentSlackManifestResponse{
			Manifest: "display_information:\n  name: support\n", AppTokenScope: "connections:write",
			Notes: []string{"Enable Socket Mode."},
		}
	}))
}

func (fc *fakeChannels) state(name string, configured bool) console.AiAgentChannelStateDto {
	if !configured {
		return console.AiAgentChannelStateDto{}
	}
	phase, last, msg, ident := fc.phase, fc.lastEvent, fc.message, "@bot:"+name
	if fc.unsettle > 0 {
		phase = console.AiAgentChannelPhaseDtoDisabled // the state from before the write
	}
	st := console.AiAgentChannelStateDto{Configured: true, Settled: fc.unsettle == 0, Phase: &phase, LastEventAt: &last, Identity: &ident}
	if msg != "" {
		st.Message = &msg
	}
	if name == "matrix" && fc.device != "" {
		d := fc.device
		st.DeviceId = &d
	}
	return st
}

func (fc *fakeChannels) response() console.AiAgentChannelsResponse {
	return console.AiAgentChannelsResponse{
		Matrix: console.AiAgentMatrixChannelView{
			Config: fc.matrix, State: fc.state("matrix", fc.matrix != nil),
			ReloginPending: fc.pending, ReloginRequired: fc.required,
		},
		Slack:         console.AiAgentSlackChannelView{Config: fc.slack, State: fc.state("slack", fc.slack != nil)},
		Discord:       console.AiAgentDiscordChannelView{Config: fc.discord, State: fc.state("discord", fc.discord != nil)},
		ChannelLimits: fc.limits,
	}
}

func strSet(values ...string) types.Set {
	out := make([]attr.Value, 0, len(values))
	for _, v := range values {
		out = append(out, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, out)
}

var (
	testPasswordSecret = "7D0A8C3E-0000-4000-8000-000000000001" // upper case on purpose: the console answers lowercase
	testRecoverySecret = uuid.NewString()
)

func unknownStatus() channelStatusModel {
	return channelStatusModel{
		Configured: types.BoolUnknown(), Phase: types.StringUnknown(), Identity: types.StringUnknown(),
		Message: types.StringUnknown(), LastEventAt: types.StringUnknown(),
	}
}

func matrixPlan(fc *fakeChannels) agentMatrixChannelModel {
	return agentMatrixChannelModel{
		ID: types.StringUnknown(), AgentID: types.StringValue(fc.agent.String()),
		Preset: types.StringValue("tchap"), HomeserverURL: types.StringNull(), UserID: types.StringValue("@bot-support:agent.example.tchap.gouv.fr"),
		PasswordSecretID: types.StringValue(testPasswordSecret), RecoveryKeySecretID: types.StringValue(testRecoverySecret),
		ExtraAuthHosts: types.SetNull(types.StringType), AllowedRooms: types.SetNull(types.StringType),
		AllowedUsers: strSet("@*:example.org"), AutoJoin: types.StringNull(), DirectMessages: types.BoolValue(true),
		UnencryptedRooms: types.StringValue("never"), AllowExternalInference: types.BoolValue(false),
		ReloginTrigger:     types.StringValue("1"),
		channelStatusModel: unknownStatus(),
		DeviceID:           types.StringUnknown(), ReloginRequired: types.BoolUnknown(), ReloginPending: types.BoolUnknown(),
	}
}

func TestAgentChannelSchemas(t *testing.T) {
	for _, r := range []fwresource.Resource{
		NewAgentMatrixChannelResource(), NewAgentSlackChannelResource(), NewAgentDiscordChannelResource(), NewAgentChannelLimitsResource(),
	} {
		assertSchemaImplementation(t, r)
	}
}

// Create sets the channel with exactly the configured fields, references the
// secrets by id only, does not ask for a re-login, and reports the status.
func TestAgentMatrixChannelCreate(t *testing.T) {
	fc, pd := newFakeChannels(t)
	l := newLifecycle[agentMatrixChannelModel](t, wired[*agentMatrixChannelResource](NewAgentMatrixChannelResource(), pd))
	got, d := l.create(matrixPlan(fc))
	mustSucceed(t, d)

	body := fc.calls("PUT", `/channels/matrix`)[0].body
	if body["password_secret_id"] != strings.ToLower(testPasswordSecret) || body["preset"] != "tchap" || body["direct_messages"] != true {
		t.Errorf("PUT body = %v", body)
	}
	for _, k := range []string{"homeserver_url", "allowed_rooms", "auto_join", "extra_auth_hosts"} {
		if _, ok := body[k]; ok {
			t.Errorf("PUT carries %q, which the config does not set", k)
		}
	}
	for k, v := range body {
		if strings.Contains(k, "password") && k != "password_secret_id" || v == "hunter2" {
			t.Errorf("PUT carries a secret value under %q", k)
		}
	}
	if n := len(fc.calls("POST", `/relogin`)); n != 0 {
		t.Errorf("create asked for %d re-login(s); the first login needs none", n)
	}
	if got.ID.ValueString() != fc.agent.String() || got.PasswordSecretID.ValueString() != testPasswordSecret ||
		!got.AllowExternalInference.Equal(types.BoolValue(false)) || got.ReloginTrigger.ValueString() != "1" {
		t.Errorf("state = %+v", got)
	}
	if got.Phase.ValueString() != "Connecting" || !got.Configured.ValueBool() || got.LastEventAt.ValueString() != "2026-11-16T09:00:00Z" || !got.DeviceID.IsNull() {
		t.Errorf("status = %+v", got.channelStatusModel)
	}
	if !slices.Contains(fc.interfaces, "matrix") {
		t.Error("the fake did not list the interface")
	}
}

// The status moves on its own: a refresh records it and leaves the
// configuration as written.
func TestAgentMatrixChannelStatusRefresh(t *testing.T) {
	fc, pd := newFakeChannels(t)
	l := newLifecycle[agentMatrixChannelModel](t, wired[*agentMatrixChannelResource](NewAgentMatrixChannelResource(), pd))
	created, d := l.create(matrixPlan(fc))
	mustSucceed(t, d)

	fc.phase, fc.device, fc.message = console.AiAgentChannelPhaseDtoConnected, "DEVICEA", "in 3 rooms"
	fc.lastEvent = fc.lastEvent.Add(time.Hour)
	refreshed, removed, d := l.read(created)
	mustSucceed(t, d)
	if removed || refreshed.Phase.ValueString() != "Connected" || refreshed.DeviceID.ValueString() != "DEVICEA" ||
		refreshed.Message.ValueString() != "in 3 rooms" || refreshed.LastEventAt.ValueString() != "2026-11-16T10:00:00Z" {
		t.Errorf("refreshed status = %+v %s", refreshed.channelStatusModel, refreshed.DeviceID)
	}
	cfg := func(m agentMatrixChannelModel) agentMatrixChannelModel {
		m.channelStatusModel, m.DeviceID, m.ReloginPending, m.ReloginRequired = channelStatusModel{}, types.String{}, types.Bool{}, types.Bool{}
		return m
	}
	if !reflect.DeepEqual(cfg(refreshed), cfg(created)) {
		t.Errorf("a refresh changed the configuration:\n%+v\n%+v", cfg(refreshed), cfg(created))
	}

	fc.matrix = nil
	if _, removed, d = l.read(created); d.HasError() || !removed {
		t.Error("a channel removed outside Terraform stayed in state")
	}
}

// A new relogin_trigger asks for a login only when the platform takes one
// (required, none pending); otherwise the value is recorded with a warning and
// nothing is sent. A refusal is a clear error that leaves the old trigger, so
// the next apply asks again, and keeps what the apply already changed.
func TestAgentMatrixChannelRelogin(t *testing.T) {
	fc, pd := newFakeChannels(t)
	l := newLifecycle[agentMatrixChannelModel](t, wired[*agentMatrixChannelResource](NewAgentMatrixChannelResource(), pd))
	created, d := l.create(matrixPlan(fc))
	mustSucceed(t, d)

	bump := func(prior agentMatrixChannelModel, trigger string) agentMatrixChannelModel {
		next := prior
		next.ReloginTrigger = types.StringValue(trigger)
		return next
	}

	// Not required (the bot is logged in): nothing sent, trigger recorded.
	notNeeded, d := l.update(bump(created, "2"), created)
	mustSucceed(t, d)
	if fc.relogins != 0 || len(fc.calls("POST", `/relogin`)) != 0 || notNeeded.ReloginTrigger.ValueString() != "2" {
		t.Fatalf("relogins = %d, trigger = %s", fc.relogins, notNeeded.ReloginTrigger)
	}
	if len(d.Warnings()) == 0 || !strings.Contains(d.Warnings()[0].Summary(), "does not need a re-login") {
		t.Errorf("warnings = %v", d.Warnings())
	}

	// Required: one login, and it is pending.
	fc.device, fc.required = "DEVICEA", true
	relogged, d := l.update(bump(notNeeded, "3"), notNeeded)
	mustSucceed(t, d)
	if fc.relogins != 1 || relogged.ReloginTrigger.ValueString() != "3" || !relogged.ReloginPending.ValueBool() {
		t.Fatalf("relogins = %d, state = %+v", fc.relogins, relogged)
	}

	// Pending: nothing sent again.
	pending, d := l.update(bump(relogged, "4"), relogged)
	mustSucceed(t, d)
	if fc.relogins != 1 || pending.ReloginTrigger.ValueString() != "4" || !strings.Contains(d.Warnings()[0].Summary(), "already pending") {
		t.Errorf("relogins = %d, warnings = %v", fc.relogins, d.Warnings())
	}

	// Removing the trigger does not log in.
	removed := pending
	removed.ReloginTrigger = types.StringNull()
	if _, d = l.update(removed, pending); d.HasError() || fc.relogins != 1 {
		t.Errorf("removing the trigger logged in again (%d) or failed: %v", fc.relogins, d)
	}

	// Required, but the platform refuses (a channel without its device, say):
	// the configuration change is kept, the trigger is not.
	fc.pending, fc.required, fc.device = false, true, ""
	refusedPlan := bump(pending, "5")
	refusedPlan.AllowedUsers = strSet("@*:agent.example.tchap.gouv.fr", "@ops:agent.example.tchap.gouv.fr")
	refused, d := l.update(refusedPlan, pending)
	mustFail(t, d, "has not completed its first login")
	if d.Errors()[0].Summary() != "The Matrix re-login was refused" || refused.ReloginTrigger.ValueString() != "4" ||
		len(refused.AllowedUsers.Elements()) != 2 {
		t.Errorf("summary = %q, state = %+v", d.Errors()[0].Summary(), refused)
	}
}

// Binding a secret, or asking for a re-login with it, needs secret:access on
// it; the 403 names that permission.
func TestAgentChannelSecretAccess(t *testing.T) {
	fc, pd := newFakeChannels(t)
	l := newLifecycle[agentMatrixChannelModel](t, wired[*agentMatrixChannelResource](NewAgentMatrixChannelResource(), pd))
	fc.forbidSecrets = true
	_, d := l.create(matrixPlan(fc))
	mustFail(t, d, "Missing permission `secret:access`")
	if !strings.Contains(d.Errors()[0].Detail(), "password_secret_id") {
		t.Errorf("detail = %s", d.Errors()[0].Detail())
	}

	fc.forbidSecrets = false
	created, d := l.create(matrixPlan(fc))
	mustSucceed(t, d)
	fc.device, fc.required, fc.forbidSecrets = "DEVICEA", true, true
	next := created
	next.ReloginTrigger = types.StringValue("2")
	_, d = l.update(next, created)
	mustFail(t, d, "Missing permission `secret:access`")
}

// After a write, the status recorded is the one that answers it: the apply
// waits for settled instead of reading the state from before the write, and
// goes on with a warning if the operator does not catch up in time.
func TestAgentChannelWaitsForSettled(t *testing.T) {
	fc, pd := newFakeChannels(t)
	fc.lag = 3
	l := newLifecycle[agentSlackChannelModel](t, wired[*agentSlackChannelResource](NewAgentSlackChannelResource(), pd))
	got, d := l.create(slackPlan(fc))
	mustSucceed(t, d)
	if got.Phase.ValueString() != "Connecting" {
		t.Errorf("phase = %s: the state from before the write was recorded", got.Phase)
	}
	if n := len(fc.calls("GET", `/ai-agents/[^/]+/channels`)); n != 3 {
		t.Errorf("GETs = %d, want a poll until settled", n)
	}

	fc2, pd2 := newFakeChannels(t)
	fc2.lag = 1 << 20
	r := wired[*agentDiscordChannelResource](NewAgentDiscordChannelResource(), pd2)
	r.timeout = 20 * time.Millisecond
	_, d = newLifecycle[agentDiscordChannelModel](t, r).create(agentDiscordChannelModel{
		ID: types.StringUnknown(), AgentID: types.StringValue(fc2.agent.String()), BotTokenSecretID: types.StringValue(uuid.NewString()),
		AllowedGuilds: strSet("123456789"), AllowedChannels: types.SetNull(types.StringType), AllowedUsers: types.SetNull(types.StringType),
		DirectMessages: types.BoolNull(), channelStatusModel: unknownStatus(),
	})
	mustSucceed(t, d)
	if len(d.Warnings()) == 0 || !strings.Contains(d.Warnings()[0].Summary(), "has not settled") {
		t.Errorf("warnings = %v", d.Warnings())
	}
}

// Destroy removes the channel and its interface; an agent that is gone is fine.
func TestAgentMatrixChannelDelete(t *testing.T) {
	fc, pd := newFakeChannels(t)
	l := newLifecycle[agentMatrixChannelModel](t, wired[*agentMatrixChannelResource](NewAgentMatrixChannelResource(), pd))
	created, d := l.create(matrixPlan(fc))
	mustSucceed(t, d)
	mustSucceed(t, l.destroy(created))
	if fc.matrix != nil || slices.Contains(fc.interfaces, "matrix") {
		t.Errorf("destroy left the channel (%v) or its interface (%v)", fc.matrix, fc.interfaces)
	}
	fc.agentGone = true
	mustSucceed(t, l.destroy(created))
}

func TestAgentMatrixChannelValidateConfig(t *testing.T) {
	fc, pd := newFakeChannels(t)
	r := wired[*agentMatrixChannelResource](NewAgentMatrixChannelResource(), pd)
	l := newLifecycle[agentMatrixChannelModel](t, r)
	validate := func(m agentMatrixChannelModel) []string {
		resp := &fwresource.ValidateConfigResponse{}
		r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: l.config(m)}, resp)
		var out []string
		for _, e := range resp.Diagnostics.Errors() {
			out = append(out, e.Summary())
		}
		return out
	}
	if errs := validate(matrixPlan(fc)); len(errs) != 0 {
		t.Errorf("a valid Tchap channel was refused: %v", errs)
	}
	generic := matrixPlan(fc)
	generic.Preset = types.StringNull()
	generic.AllowedUsers = types.SetNull(types.StringType)
	generic.AllowExternalInference = types.BoolValue(true)
	errs := strings.Join(validate(generic), " | ")
	for _, want := range []string{"Missing homeserver_url", "No allowlist", "Tchap only", "Direct messages need allowed_users"} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in %s", want, errs)
		}
	}

	tchap := matrixPlan(fc)
	tchap.HomeserverURL = types.StringValue("https://matrix.example.org")
	tchap.UserID = types.StringValue("@bot:example.org")
	errs = strings.Join(validate(tchap), " | ")
	for _, want := range []string{"homeserver_url with the tchap preset", "Not a Tchap account"} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in %s", want, errs)
		}
	}
}

func slackPlan(fc *fakeChannels) agentSlackChannelModel {
	return agentSlackChannelModel{
		ID: types.StringUnknown(), AgentID: types.StringValue(fc.agent.String()),
		BotTokenSecretID: types.StringValue(uuid.NewString()), AppTokenSecretID: types.StringValue(uuid.NewString()),
		TeamID: types.StringValue("T0123"), AllowedChannels: strSet("C1", "C2"), AllowedUsers: strSet("U1"),
		DirectMessages: types.BoolValue(false), channelStatusModel: unknownStatus(),
	}
}

// A Slack update sends the whole block, so a removed allowlist is cleared;
// import recovers the configuration by agent id.
func TestAgentSlackChannelLifecycle(t *testing.T) {
	fc, pd := newFakeChannels(t)
	l := newLifecycle[agentSlackChannelModel](t, wired[*agentSlackChannelResource](NewAgentSlackChannelResource(), pd))
	created, d := l.create(slackPlan(fc))
	mustSucceed(t, d)
	if created.TeamID.ValueString() != "T0123" || !created.DirectMessages.Equal(types.BoolValue(false)) || created.Identity.ValueString() != "@bot:slack" {
		t.Errorf("created = %+v", created)
	}

	next := created
	next.AllowedUsers = types.SetNull(types.StringType)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	body := fc.calls("PUT", `/channels/slack`)[1].body
	if _, ok := body["allowed_users"]; ok {
		t.Errorf("PUT still carries allowed_users, which was removed: %v", body)
	}
	if !updated.AllowedUsers.IsNull() {
		t.Errorf("allowed_users = %s", updated.AllowedUsers)
	}

	imported, d := l.importAndRead(fc.agent.String())
	mustSucceed(t, d)
	if imported.BotTokenSecretID.ValueString() != created.BotTokenSecretID.ValueString() || len(imported.AllowedChannels.Elements()) != 2 {
		t.Errorf("imported = %+v", imported)
	}
	mustSucceed(t, l.destroy(updated))
	if fc.slack != nil || slices.Contains(fc.interfaces, "slack") {
		t.Error("destroy left the Slack channel")
	}
}

// The platform's refusal reaches the user in its words.
func TestAgentDiscordChannelRefused(t *testing.T) {
	fc, pd := newFakeChannels(t)
	fc.refuse = "the tool list of this agent includes general hfctl: set allow_platform_tools on the agent to let a chat channel drive it"
	l := newLifecycle[agentDiscordChannelModel](t, wired[*agentDiscordChannelResource](NewAgentDiscordChannelResource(), pd))
	_, d := l.create(agentDiscordChannelModel{
		ID: types.StringUnknown(), AgentID: types.StringValue(fc.agent.String()), BotTokenSecretID: types.StringValue(uuid.NewString()),
		AllowedGuilds: strSet("123456789"), AllowedChannels: types.SetNull(types.StringType), AllowedUsers: types.SetNull(types.StringType),
		DirectMessages: types.BoolNull(), channelStatusModel: unknownStatus(),
	})
	mustFail(t, d, "set allow_platform_tools on the agent")
	if s := d.Errors()[0].Summary(); !strings.HasPrefix(s, "The platform refused") {
		t.Errorf("summary = %q", s)
	}
}

func TestAgentDiscordChannelCreate(t *testing.T) {
	fc, pd := newFakeChannels(t)
	fc.phase, fc.message = console.AiAgentChannelPhaseDtoDegraded, "the bot is not in guild 123456789"
	l := newLifecycle[agentDiscordChannelModel](t, wired[*agentDiscordChannelResource](NewAgentDiscordChannelResource(), pd))
	got, d := l.create(agentDiscordChannelModel{
		ID: types.StringUnknown(), AgentID: types.StringValue(fc.agent.String()), BotTokenSecretID: types.StringValue(uuid.NewString()),
		AllowedGuilds: strSet("123456789"), AllowedChannels: types.SetNull(types.StringType), AllowedUsers: types.SetNull(types.StringType),
		DirectMessages: types.BoolNull(), channelStatusModel: unknownStatus(),
	})
	mustSucceed(t, d)
	if got.Phase.ValueString() != "Degraded" || !slices.Contains(fc.interfaces, "discord") {
		t.Errorf("state = %+v", got)
	}
	if len(d.Warnings()) == 0 || !strings.Contains(d.Warnings()[0].Detail(), "not in guild") {
		t.Errorf("the apply did not say the channel is degraded: %v", d.Warnings())
	}
}

func limitsPlan(fc *fakeChannels) agentChannelLimitsModel {
	return agentChannelLimitsModel{
		ID: types.StringUnknown(), AgentID: types.StringValue(fc.agent.String()),
		QuestionsPerHour: types.Int64Value(200), QuestionsPerDay: types.Int64Value(1000),
		PerSenderBurst: types.Int64Null(), PerSenderRefillSeconds: types.Int64Null(),
		MaxQuestionLength: types.Int64Value(2000), MaxConcurrent: types.Int64Null(),
	}
}

// Limits: set, change (a removed limit returns to the default), reset on
// destroy, and leave the state when reset outside Terraform.
func TestAgentChannelLimitsLifecycle(t *testing.T) {
	fc, pd := newFakeChannels(t)
	l := newLifecycle[agentChannelLimitsModel](t, wired[*agentChannelLimitsResource](NewAgentChannelLimitsResource(), pd))
	created, d := l.create(limitsPlan(fc))
	mustSucceed(t, d)
	if created.QuestionsPerHour.ValueInt64() != 200 || !created.PerSenderBurst.IsNull() || created.ID.ValueString() != fc.agent.String() {
		t.Errorf("created = %+v", created)
	}

	next := created
	next.QuestionsPerDay = types.Int64Null()
	next.MaxConcurrent = types.Int64Value(4)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	body := fc.calls("PUT", `/channel-limits`)[1].body
	if _, ok := body["questions_per_day"]; ok || body["max_concurrent"] != float64(4) {
		t.Errorf("PUT body = %v", body)
	}
	if !updated.QuestionsPerDay.IsNull() {
		t.Errorf("updated = %+v", updated)
	}

	imported, d := l.importAndRead(fc.agent.String())
	mustSucceed(t, d)
	if imported.MaxConcurrent.ValueInt64() != 4 {
		t.Errorf("imported = %+v", imported)
	}

	mustSucceed(t, l.destroy(updated))
	if fc.limits != nil || len(fc.calls("DELETE", `/channel-limits`)) != 1 {
		t.Error("destroy did not reset the limits")
	}
	if _, removed, d := l.read(updated); d.HasError() || !removed {
		t.Error("limits reset outside Terraform stayed in state")
	}

	r := wired[*agentChannelLimitsResource](NewAgentChannelLimitsResource(), pd)
	resp := &fwresource.ValidateConfigResponse{}
	empty := limitsPlan(fc)
	empty.QuestionsPerHour, empty.QuestionsPerDay, empty.MaxQuestionLength = types.Int64Null(), types.Int64Null(), types.Int64Null()
	r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: l.config(empty)}, resp)
	mustFail(t, resp.Diagnostics, "Set at least one of")
}

func TestAgentSlackManifestDataSource(t *testing.T) {
	fc, pd := newFakeChannels(t)
	ds := &agentSlackManifestDataSource{p: pd}
	got, d := readDataSource(t, ds, agentSlackManifestModel{
		AgentID: types.StringValue(fc.agent.String()), Manifest: types.StringNull(), AppTokenScope: types.StringNull(),
		Notes: types.ListNull(types.StringType),
	})
	mustSucceed(t, d)
	if !strings.Contains(got.Manifest.ValueString(), "display_information") || got.AppTokenScope.ValueString() != "connections:write" ||
		len(got.Notes.Elements()) != 1 {
		t.Errorf("manifest = %+v", got)
	}
}

func (b *agentChannelBase) base() *agentChannelBase { return b }

// wired connects a channel resource to a test provider.
func wired[T fwresource.Resource](r fwresource.Resource, pd *providerData) T {
	b, ok := r.(interface{ base() *agentChannelBase })
	if !ok {
		panic("not a channel resource")
	}
	b.base().p, b.base().interval = pd, time.Millisecond
	out, ok := r.(T)
	if !ok {
		panic("unexpected resource type")
	}
	return out
}

// The Tchap server rule mirrors the platform's (is_tchap_server_name).
func TestIsTchapUserID(t *testing.T) {
	for mxid, want := range map[string]bool{
		"@bot:agent.example.tchap.gouv.fr":      true,
		"@bot:agent.example.tchap.gouv.fr:8448": true,
		"@bot:Agent.Example.Tchap.Gouv.Fr":      true,
		"@bot:tchap.gouv.fr":                    false,
		"@bot:eviltchap.gouv.fr":                false,
		"@bot:tchap.gouv.fr.attacker.example":   false,
		"@bot:attacker.example":                 false,
		"bot":                                   false,
	} {
		if got := isTchapUserID(mxid); got != want {
			t.Errorf("isTchapUserID(%q) = %v, want %v", mxid, got, want)
		}
	}
}
