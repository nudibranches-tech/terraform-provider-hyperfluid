// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeAgent serves one agent, created by the first POST. Like the API, it
// fills an omitted system prompt, tool set and interface list, and reports
// Provisioning once before the agent runs.
type fakeAgent struct {
	*fakeAPI
	agent console.AiAgentResponse
	polls int
	phase string
	gone  bool
}

func newFakeAgent(t *testing.T) (*fakeAgent, *agentResource) {
	f, pd := newFakeAPI(t)
	fa := &fakeAgent{fakeAPI: f, phase: "Running"}
	f.on("POST", `/ai-agents`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateAiAgentRequest](t, raw)
		fa.agent = console.AiAgentResponse{
			Id: uuid.New(), HarborId: req.HarborId, OrganizationId: f.org, Name: *req.Name, Slug: *req.Name,
			Description: req.Description, SystemPrompt: "You answer platform questions.", Speciality: req.Speciality,
			QuestionSet: derefStrings(req.QuestionSet), Tools: []string{"ask_sender"}, Inference: req.Inference,
			DataSources: []console.AiAgentDataSourceDto{}, DesiredState: console.AiAgentDesiredStateDtoRunning,
			Interfaces: []string{"web"}, ServiceAccountId: req.ServiceAccountId,
			Status: console.AiAgentStatusResponse{Phase: "Provisioning"}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if req.SystemPrompt != nil {
			fa.agent.SystemPrompt = *req.SystemPrompt
		}
		if req.Budgets != nil {
			fa.agent.Budgets = *req.Budgets
		}
		return 201, fa.agent
	})
	f.on("GET", `/ai-agents/[^/]+`, func(string, []byte) (int, any) {
		if fa.gone {
			return 404, map[string]string{"message": "not found"}
		}
		fa.polls++
		if fa.polls > 1 {
			fa.agent.Status.Phase = fa.phase
		}
		return 200, fa.agent
	})
	f.on("PATCH", `/ai-agents/[^/]+`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.UpdateAiAgentRequest](t, raw)
		if req.Description != nil {
			fa.agent.Description = req.Description
		}
		if req.Budgets != nil {
			fa.agent.Budgets = *req.Budgets
		}
		if req.DetachServiceAccount != nil && *req.DetachServiceAccount {
			fa.agent.ServiceAccountId = nil
		}
		if req.DesiredState != nil {
			fa.agent.DesiredState = *req.DesiredState
			fa.phase = string(*req.DesiredState)
		}
		if req.Interfaces != nil {
			fa.agent.Interfaces = *req.Interfaces
		}
		if req.AllowPlatformTools != nil {
			fa.agent.AllowPlatformTools = req.AllowPlatformTools
		}
		return 200, fa.agent
	})
	f.on("DELETE", `/ai-agents/[^/]+`, func(string, []byte) (int, any) {
		fa.gone = true
		return 204, nil
	})
	return fa, &agentResource{p: pd, interval: time.Millisecond}
}

func agentPlan(f *fakeAPI) agentModel {
	return agentModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("ops"),
		SystemPrompt: types.StringUnknown(), Speciality: types.StringValue("platform-ops"),
		QuestionSet: types.ListNull(types.StringType), Tools: types.ListUnknown(types.StringType),
		Inference: types.ObjectValueMust(agentInferenceType.AttrTypes, map[string]attr.Value{
			"provider": types.StringValue("sapience"), "model": types.StringValue("sapience:shared/qwen"),
			"api_key_secret_id": types.StringNull(), "endpoint_url": types.StringNull(),
		}),
		Budgets: types.ObjectNull(agentBudgetsType.AttrTypes), DataSources: types.ListNull(agentDockSourceType),
		DesiredState: types.StringUnknown(), Interfaces: types.SetUnknown(types.StringType),
		Mail: types.ObjectUnknown(agentMailType.AttrTypes),
		Slug: types.StringUnknown(), Phase: types.StringUnknown(), StatusMessage: types.StringUnknown(), MailAddress: types.StringUnknown(),
	}
}

func TestAgentSchema(t *testing.T) {
	assertSchemaImplementation(t, NewAgentResource())
}

// What the platform fills in stays out of the request and reads back as the
// platform chose it; the create waits for the agent to run.
func TestAgentCreate(t *testing.T) {
	f, r := newFakeAgent(t)
	l := newLifecycle[agentModel](t, r)
	got, d := l.create(agentPlan(f.fakeAPI))
	mustSucceed(t, d)
	body := f.calls("POST", `/ai-agents`)[0].body
	for _, k := range []string{"system_prompt", "tools", "interfaces", "desired_state", "budgets", "mail"} {
		if _, ok := body[k]; ok {
			t.Errorf("create body carries %q, which the config does not set: %v", k, body)
		}
	}
	if got.Phase.ValueString() != "Running" || got.SystemPrompt.ValueString() == "" || !got.Budgets.IsNull() || !got.Mail.IsNull() {
		t.Errorf("state = %+v", got)
	}
	if f.polls < 2 {
		t.Errorf("polled %d times, want the wait to pass Provisioning", f.polls)
	}
}

// Removing budgets and the service account clears them; stopping the agent
// settles on Stopped.
func TestAgentUpdate(t *testing.T) {
	f, r := newFakeAgent(t)
	l := newLifecycle[agentModel](t, r)
	plan := agentPlan(f.fakeAPI)
	plan.Budgets = types.ObjectValueMust(agentBudgetsType.AttrTypes, map[string]attr.Value{
		"max_turns": types.Int64Value(5), "max_completion_tokens": types.Int64Null(),
	})
	plan.ServiceAccountID = types.StringValue(uuid.NewString())
	created, d := l.create(plan)
	mustSucceed(t, d)
	if !created.Budgets.Equal(plan.Budgets) {
		t.Fatalf("budgets = %v", created.Budgets)
	}

	next := created
	next.Budgets = types.ObjectNull(agentBudgetsType.AttrTypes)
	next.ServiceAccountID = types.StringNull()
	next.DesiredState = types.StringValue("Stopped")
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	body := f.calls("PATCH", `/ai-agents/[^/]+`)[0].body
	if b, ok := body["budgets"].(map[string]any); !ok || len(b) != 0 {
		t.Errorf("PATCH budgets = %v, want {}", body["budgets"])
	}
	if body["detach_service_account"] != true {
		t.Errorf("PATCH = %v, want the service account detached", body)
	}
	if _, ok := body["inference"]; ok {
		t.Error("PATCH carries inference, which did not change")
	}
	if updated.Phase.ValueString() != "Stopped" || !updated.Budgets.IsNull() || !updated.ServiceAccountID.IsNull() {
		t.Errorf("updated = %+v", updated)
	}
}

// A Failed agent ends the wait with the operator's reason.
func TestAgentFailedFailsFast(t *testing.T) {
	f, r := newFakeAgent(t)
	f.phase = "Failed"
	l := newLifecycle[agentModel](t, r)
	_, d := l.create(agentPlan(f.fakeAPI))
	mustFail(t, d, "failed to start")
}

func TestAgentImportAndDrift(t *testing.T) {
	f, r := newFakeAgent(t)
	l := newLifecycle[agentModel](t, r)
	created, d := l.create(agentPlan(f.fakeAPI))
	mustSucceed(t, d)
	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported.Name.ValueString() != "ops" || imported.Speciality.ValueString() != "platform-ops" {
		t.Errorf("imported = %+v", imported)
	}
	f.gone = true
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("an agent deleted outside Terraform stayed in state")
	}
}

func TestAgentDelete(t *testing.T) {
	f, r := newFakeAgent(t)
	l := newLifecycle[agentModel](t, r)
	created, d := l.create(agentPlan(f.fakeAPI))
	mustSucceed(t, d)
	mustSucceed(t, l.destroy(created))
}

func TestAgentFeatureDisabled(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("POST", `/ai-agents`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": "Your organization does not have access to the AI agents feature"}
	})
	l := newLifecycle[agentModel](t, &agentResource{p: pd})
	_, d := l.create(agentPlan(f))
	mustFail(t, d, "sapience_agent_enabled")
}

// A permission denial is not mistaken for a disabled feature.
func TestAgentPermissionDeniedIsNotAFeatureError(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("POST", `/ai-agents`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": "Missing permission 'ai_agent:create'"}
	})
	l := newLifecycle[agentModel](t, &agentResource{p: pd})
	_, d := l.create(agentPlan(f))
	mustFail(t, d, "ai_agent:create")
	for _, e := range d.Errors() {
		if e.Summary() != "Failed to create agent" {
			t.Errorf("summary = %q", e.Summary())
		}
	}
}

// The chat channels' interfaces belong to their own resources: the agent's
// `interfaces` does not report them (so a channel never plans a change on the
// agent), and an update sends the configured list as it is, since the
// platform ignores channel interfaces in the PATCH and keeps its own.
func TestAgentInterfacesLeaveChannelsOut(t *testing.T) {
	f, r := newFakeAgent(t)
	l := newLifecycle[agentModel](t, r)
	plan := agentPlan(f.fakeAPI)
	plan.Interfaces = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("web")})
	created, d := l.create(plan)
	mustSucceed(t, d)

	f.agent.Interfaces = append(f.agent.Interfaces, "matrix", "slack") // set by the channel resources
	refreshed, _, d := l.read(created)
	mustSucceed(t, d)
	if !refreshed.Interfaces.Equal(plan.Interfaces) {
		t.Errorf("interfaces = %s: the channels' interfaces are reported", refreshed.Interfaces)
	}

	next := refreshed
	next.Interfaces = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("web"), types.StringValue("mail")})
	_, d = l.update(next, refreshed)
	mustSucceed(t, d)
	sent, _ := f.calls("PATCH", `/ai-agents/[^/]+`)[0].body["interfaces"].([]any)
	if len(sent) != 2 {
		t.Errorf("PATCH interfaces = %v, want exactly the configured web and mail", sent)
	}

	// The schema no longer offers the channel values.
	s := resourceSchema(t, NewAgentResource()).Schema
	cfg := nullConfig(t, s, nil)
	attrSchema, _ := s.AttributeAtPath(t.Context(), path.Root("interfaces"))
	setAttr, ok := attrSchema.(schema.SetAttribute)
	if !ok {
		t.Fatal("interfaces is not a set attribute")
	}
	for _, v := range []string{"slack", "matrix"} {
		resp := &validator.SetResponse{}
		req := validator.SetRequest{
			Path: path.Root("interfaces"), PathExpression: path.Root("interfaces").Expression(), Config: cfg,
			ConfigValue: types.SetValueMust(types.StringType, []attr.Value{types.StringValue(v)}),
		}
		for _, val := range setAttr.Validators {
			val.ValidateSet(t.Context(), req, resp)
		}
		if !resp.Diagnostics.HasError() {
			t.Errorf("interfaces accepts %q", v)
		}
	}
}

// Turning allow_platform_tools on needs a permission on service accounts; the
// diagnostic names the one the platform reports, whichever it is.
func TestAgentAllowPlatformToolsPermission(t *testing.T) {
	for _, permission := range []string{"service_account:update", "service_account:read"} {
		t.Run(permission, func(t *testing.T) {
			f, r := newFakeAgent(t)
			l := newLifecycle[agentModel](t, r)
			created, d := l.create(agentPlan(f.fakeAPI))
			mustSucceed(t, d)
			f.on("PATCH", `/ai-agents/[^/]+`, func(string, []byte) (int, any) {
				return 403, map[string]string{"message": "Missing permission '" + permission + "'"}
			})
			// The first PATCH route still answers: route the refusal first.
			f.routes = append(f.routes[len(f.routes)-1:], f.routes[:len(f.routes)-1]...)
			next := created
			next.AllowPlatformTools = types.BoolValue(true)
			_, d = l.update(next, created)
			mustFail(t, d, "Missing permission `"+permission+"`")
			if !strings.Contains(d.Errors()[0].Detail(), "allow_platform_tools") {
				t.Errorf("detail = %s", d.Errors()[0].Detail())
			}
		})
	}
}

// allow_platform_tools is set right after create (the create request has no
// field for it), changed in place, and left alone when omitted.
func TestAgentAllowPlatformTools(t *testing.T) {
	f, r := newFakeAgent(t)
	l := newLifecycle[agentModel](t, r)
	plan := agentPlan(f.fakeAPI)
	plan.AllowPlatformTools = types.BoolValue(true)
	created, d := l.create(plan)
	mustSucceed(t, d)
	patches := f.calls("PATCH", `/ai-agents/[^/]+`)
	if len(patches) != 1 || patches[0].body["allow_platform_tools"] != true || !created.AllowPlatformTools.ValueBool() {
		t.Fatalf("patches = %v, state = %s", patches, created.AllowPlatformTools)
	}

	next := created
	next.AllowPlatformTools = types.BoolValue(false)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if f.calls("PATCH", `/ai-agents/[^/]+`)[1].body["allow_platform_tools"] != false || updated.AllowPlatformTools.ValueBool() {
		t.Errorf("updated = %s", updated.AllowPlatformTools)
	}

	f2, r2 := newFakeAgent(t)
	got, d := newLifecycle[agentModel](t, r2).create(agentPlan(f2.fakeAPI))
	mustSucceed(t, d)
	if n := len(f2.calls("PATCH", `/ai-agents/[^/]+`)); n != 0 || got.AllowPlatformTools.ValueBool() {
		t.Errorf("an agent without the attribute was patched %d time(s)", n)
	}
}
