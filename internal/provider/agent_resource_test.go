// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
