// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
	"time"

	"github.com/google/uuid"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeVirtualMachineUser serves the guest users of one virtual machine. The
// machine refuses users until notReady creates have been tried, the way a
// guest whose agent is still connecting does; a user reports Provisioning once.
type fakeVirtualMachineUser struct {
	*fakeAPI
	vmID     uuid.UUID
	user     console.VirtualMachineUserResponse
	notReady int
	polls    int
	gone     bool
	failWith string
}

func newFakeVirtualMachineUser(t *testing.T) (*fakeVirtualMachineUser, *virtualMachineUserResource) {
	f, pd := newFakeAPI(t)
	fu := &fakeVirtualMachineUser{fakeAPI: f, vmID: uuid.New()}
	f.on("POST", `/virtual-machines/[^/]+/users/crd`, func(_ string, raw []byte) (int, any) {
		if fu.notReady > 0 {
			fu.notReady--
			return 409, map[string]string{"message": "The guest agent is not connected, so guest users cannot be added. It must be installed and running in the image."}
		}
		req := decodeInto[console.CreateVirtualMachineUserCrdRequestBody](t, raw)
		pending := "Pending"
		fu.user = console.VirtualMachineUserResponse{
			Id: uuid.New(), OrganizationId: f.org, VirtualMachineId: fu.vmID, Username: req.Username,
			Slug: "web-" + req.Username, HasPassword: req.GeneratePassword != nil && *req.GeneratePassword,
			SshKeySecretName: req.SshPublicKeySecretName, Tags: derefStrings(req.Tags), Phase: &pending,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		return 201, fu.user
	})
	f.on("GET", `/virtual-machines/[^/]+/users/[^/]+`, func(string, []byte) (int, any) {
		if fu.gone {
			return 404, map[string]string{"message": "not found"}
		}
		fu.polls++
		if fu.polls > 1 {
			phase := "Ready"
			if fu.failWith != "" {
				phase = "Failed"
				fu.user.Conditions = []any{
					map[string]any{"type": "SecretsSynced", "status": "True", "message": "synced"},
					map[string]any{"type": "CredentialsApplied", "status": "False", "message": fu.failWith},
				}
			}
			fu.user.Phase = &phase
		}
		return 200, fu.user
	})
	f.on("DELETE", `/virtual-machines/[^/]+/users/[^/]+/crd`, func(string, []byte) (int, any) {
		fu.gone = true
		return 204, nil
	})
	return fu, &virtualMachineUserResource{p: pd, interval: time.Millisecond}
}

func virtualMachineUserPlan(vmID uuid.UUID) virtualMachineUserModel {
	return virtualMachineUserModel{
		ID: types.StringUnknown(), VirtualMachine: types.StringValue(vmID.String()), Username: types.StringValue("deploy"),
		GeneratePassword: types.BoolUnknown(), SSHPublicKeySecretName: types.StringValue("web/deploy-ssh-key"),
		Tags: types.ListNull(types.StringType), Slug: types.StringUnknown(), Phase: types.StringUnknown(),
	}
}

// The create waits out a guest whose agent has not connected, then for the
// credentials to be applied. Only the SSH key's secret name crosses the API.
func TestVirtualMachineUserCreateWaitsForTheGuest(t *testing.T) {
	f, r := newFakeVirtualMachineUser(t)
	f.notReady = 2
	got, d := newLifecycle[virtualMachineUserModel](t, r).create(virtualMachineUserPlan(f.vmID))
	mustSucceed(t, d)

	posts := f.calls("POST", `/users/crd`)
	if len(posts) != 3 {
		t.Errorf("POSTed %d times, want two refusals then the create", len(posts))
	}
	body := posts[2].body
	if body["ssh_public_key_secret_name"] != "web/deploy-ssh-key" {
		t.Errorf("create body = %v", body)
	}
	if _, ok := body["generate_password"]; ok {
		t.Errorf("create body carries generate_password, which the config does not set: %v", body)
	}
	if got.Phase.ValueString() != "Ready" || got.GeneratePassword.ValueBool() || got.Slug.ValueString() != "web-deploy" {
		t.Errorf("state = %+v", got)
	}
}

func TestVirtualMachineUserGeneratedPassword(t *testing.T) {
	f, r := newFakeVirtualMachineUser(t)
	plan := virtualMachineUserPlan(f.vmID)
	plan.GeneratePassword = types.BoolValue(true)
	plan.SSHPublicKeySecretName = types.StringNull()
	got, d := newLifecycle[virtualMachineUserModel](t, r).create(plan)
	mustSucceed(t, d)
	if f.calls("POST", `/users/crd`)[0].body["generate_password"] != true {
		t.Error("the password was not requested")
	}
	if !got.GeneratePassword.ValueBool() || !got.SSHPublicKeySecretName.IsNull() {
		t.Errorf("state = %+v", got)
	}
}

func TestVirtualMachineUserNeedsACredential(t *testing.T) {
	_, r := newFakeVirtualMachineUser(t)
	l := newLifecycle[virtualMachineUserModel](t, r)
	validate := func(m virtualMachineUserModel) *fwresource.ValidateConfigResponse {
		resp := &fwresource.ValidateConfigResponse{}
		r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: l.config(m)}, resp)
		return resp
	}
	plan := virtualMachineUserPlan(uuid.New())
	plan.GeneratePassword = types.BoolNull()
	plan.SSHPublicKeySecretName = types.StringNull()
	mustFail(t, validate(plan).Diagnostics, "Missing credential")
	plan.GeneratePassword = types.BoolValue(false)
	mustFail(t, validate(plan).Diagnostics, "Missing credential")
	plan.GeneratePassword = types.BoolValue(true)
	mustSucceed(t, validate(plan).Diagnostics)
	plan.SSHPublicKeySecretName = types.StringUnknown()
	plan.GeneratePassword = types.BoolNull()
	mustSucceed(t, validate(plan).Diagnostics)
}

func TestVirtualMachineUserFailedPhaseNamesTheCause(t *testing.T) {
	f, r := newFakeVirtualMachineUser(t)
	f.failWith = "useradd: group 'deploy' already exists"
	_, d := newLifecycle[virtualMachineUserModel](t, r).create(virtualMachineUserPlan(f.vmID))
	mustFail(t, d, "group 'deploy' already exists")
}

func TestVirtualMachineUserImportAndDrift(t *testing.T) {
	f, r := newFakeVirtualMachineUser(t)
	l := newLifecycle[virtualMachineUserModel](t, r)
	created, d := l.create(virtualMachineUserPlan(f.vmID))
	mustSucceed(t, d)

	imported, d := l.importAndRead(f.vmID.String() + "/" + created.ID.ValueString())
	mustSucceed(t, d)
	if imported.VirtualMachine.ValueString() != f.vmID.String() || imported.Username.ValueString() != "deploy" ||
		imported.SSHPublicKeySecretName.ValueString() != "web/deploy-ssh-key" {
		t.Errorf("imported = %+v", imported)
	}
	_, d = l.importAndRead(created.ID.ValueString())
	mustFail(t, d, "<parent>/<child>")

	f.gone = true
	_, removed, d := l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a user deleted outside Terraform stayed in state")
	}
}

func TestVirtualMachineUserDelete(t *testing.T) {
	f, r := newFakeVirtualMachineUser(t)
	l := newLifecycle[virtualMachineUserModel](t, r)
	created, d := l.create(virtualMachineUserPlan(f.vmID))
	mustSucceed(t, d)
	mustSucceed(t, l.destroy(created))
	if len(f.calls("DELETE", `/users/[^/]+/crd`)) != 1 {
		t.Error("the CRD was not deleted")
	}
}

func TestVirtualMachineUserFeatureDisabled(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("POST", `/users/crd`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": vmFeatureDenied}
	})
	_, d := newLifecycle[virtualMachineUserModel](t, &virtualMachineUserResource{p: pd}).create(virtualMachineUserPlan(uuid.New()))
	mustFail(t, d, "virtual_machines_enabled")
}
