// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

const vmFeatureDenied = "Your organization does not have access to the virtual machines feature"

// fakeVirtualMachine serves one virtual machine, created by the first POST. It
// reports Provisioning once, then the phase its desired running state settles in.
type fakeVirtualMachine struct {
	*fakeAPI
	vm        console.VirtualMachineResponse
	polls     int
	gone      bool
	deniedGet bool // a GET answers 403, as the console does for an id it cannot resolve
	failWith  string
}

func newFakeVirtualMachine(t *testing.T) (*fakeVirtualMachine, *virtualMachineResource) {
	f, pd := newFakeAPI(t)
	fv := &fakeVirtualMachine{fakeAPI: f}
	settle := func() {
		phase := "Stopped"
		if fv.vm.Running {
			phase = "Running"
		}
		if fv.failWith != "" {
			phase = "Failed"
			fv.vm.Conditions = []any{
				map[string]any{"type": "Ready", "status": "False", "message": fv.failWith},
			}
		}
		fv.vm.Phase = &phase
	}
	f.on("GET", `/harbors/[^/]+/virtual-machines/image-catalog`, func(string, []byte) (int, any) {
		disk, installer := console.VmImageKindDisk, console.VmImageKindInstaller
		return 200, console.VirtualMachineImageCatalog{Sources: []console.VirtualMachineImageCatalogSource{
			{Name: "platform", Images: []console.VirtualMachineImageCatalogImage{
				{Key: "ubuntu-24.04.qcow2", Description: "Ubuntu 24.04", Kind: &disk},
				{Key: "debian-13.iso", Description: "Debian 13 installer", Kind: &installer},
			}},
		}}
	})
	f.on("GET", `/harbors/[^/]+/virtual-machines`, func(string, []byte) (int, any) {
		page := console.VirtualMachinesPage{Data: []console.VirtualMachineResponse{}}
		if !fv.gone && fv.vm.Name != "" {
			page.Data = append(page.Data, fv.vm)
		}
		return 200, page
	})
	f.on("POST", `/harbors/[^/]+/virtual-machines/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateVirtualMachineCrdRequestBody](t, raw)
		provisioning := "Provisioning"
		fv.vm = console.VirtualMachineResponse{
			Id: uuid.New(), HarborId: f.env, OrganizationId: f.org, Name: req.Name, Slug: req.Name,
			Cpu: req.Cpu, Memory: req.Memory, Disk: req.Disk, ImageSourceRef: req.ImageSourceRef, ImageKey: req.ImageKey,
			UserDataSecretName: req.UserDataSecretName, Running: req.Running == nil || *req.Running,
			Description: req.Description, Tags: derefStrings(req.Tags), Phase: &provisioning,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if strings.HasSuffix(req.ImageKey, ".iso") {
			attached := console.InstallMediaAttached
			fv.vm.InstallMedia = &attached
		}
		return 201, fv.vm
	})
	f.on("GET", `/virtual-machines/[^/]+`, func(string, []byte) (int, any) {
		switch {
		case fv.deniedGet:
			return 403, map[string]string{"message": "Missing permission 'virtual_machine:read'"}
		case fv.gone:
			return 404, map[string]string{"message": "not found"}
		}
		fv.polls++
		if fv.polls > 1 {
			settle()
			ip := "192.0.2.10"
			fv.vm.IpAddress = &ip
		}
		return 200, fv.vm
	})
	f.on("PATCH", `/virtual-machines/[^/]+/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.PatchVirtualMachineCrdRequestBody](t, raw)
		if req.Running != nil {
			fv.vm.Running = *req.Running
			fv.polls = 0
		}
		if req.RemoveInstallMedia != nil && *req.RemoveInstallMedia {
			removed := console.InstallMediaRemoved
			fv.vm.InstallMedia = &removed
		}
		if req.Description != nil {
			fv.vm.Description = req.Description
		}
		if req.Tags != nil {
			fv.vm.Tags = *req.Tags
		}
		return 204, nil
	})
	f.on("DELETE", `/virtual-machines/[^/]+/crd`, func(string, []byte) (int, any) {
		fv.gone = true
		return 204, nil
	})
	return fv, &virtualMachineResource{p: pd, interval: time.Millisecond}
}

func virtualMachinePlan(f *fakeAPI) virtualMachineModel {
	return virtualMachineModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("web"),
		Tags: types.ListNull(types.StringType), CPU: types.StringValue("2"), Memory: types.StringValue("4Gi"),
		Disk: types.StringValue("40Gi"), ImageSourceRef: types.StringValue("platform"),
		ImageKey: types.StringValue("ubuntu-24.04.qcow2"), Running: types.BoolUnknown(),
		InstallMediaRemoved: types.BoolUnknown(), Slug: types.StringUnknown(), Phase: types.StringUnknown(),
		IPAddress: types.StringUnknown(), InstallMedia: types.StringUnknown(),
	}
}

func TestVirtualMachineSchema(t *testing.T) {
	assertSchemaImplementation(t, NewVirtualMachineResource())
	assertSchemaImplementation(t, NewVirtualMachineUserResource())
}

// Omitted settings stay out of the request; the create waits past Provisioning.
func TestVirtualMachineCreateLeavesDefaultsToThePlatform(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	got, d := newLifecycle[virtualMachineModel](t, r).create(virtualMachinePlan(f.fakeAPI))
	mustSucceed(t, d)

	body := f.calls("POST", `/virtual-machines/crd`)[0].body
	for _, k := range []string{"running", "user_data_secret_name", "description", "tags"} {
		if _, ok := body[k]; ok {
			t.Errorf("create body carries %q, which the config does not set: %v", k, body)
		}
	}
	if got.Phase.ValueString() != "Running" || !got.Running.ValueBool() || got.IPAddress.ValueString() != "192.0.2.10" {
		t.Errorf("state = %+v", got)
	}
	if got.InstallMediaRemoved.ValueBool() || !got.InstallMedia.IsNull() || !got.UserDataSecretName.IsNull() {
		t.Errorf("a disk image VM = %+v", got)
	}
	if f.polls < 2 {
		t.Errorf("polled %d times, want the wait to pass Provisioning", f.polls)
	}
}

// A machine created stopped settles Stopped.
func TestVirtualMachineCreateStopped(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	plan := virtualMachinePlan(f.fakeAPI)
	plan.Running = types.BoolValue(false)
	plan.UserDataSecretName = types.StringValue("web/cloud-init")
	got, d := newLifecycle[virtualMachineModel](t, r).create(plan)
	mustSucceed(t, d)
	body := f.calls("POST", `/virtual-machines/crd`)[0].body
	if body["running"] != false || body["user_data_secret_name"] != "web/cloud-init" {
		t.Errorf("create body = %v", body)
	}
	if got.Phase.ValueString() != "Stopped" || got.UserDataSecretName.ValueString() != "web/cloud-init" {
		t.Errorf("state = %+v", got)
	}
}

// Stopping sends only `running` and waits for Stopped; a removed description
// and tag list are cleared.
func TestVirtualMachineUpdate(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	l := newLifecycle[virtualMachineModel](t, r)
	plan := virtualMachinePlan(f.fakeAPI)
	plan.Description = types.StringValue("front end")
	plan.Tags, _ = types.ListValueFrom(t.Context(), types.StringType, []string{"web"})
	created, d := l.create(plan)
	mustSucceed(t, d)

	next := created
	next.Running = types.BoolValue(false)
	next.Description = types.StringNull()
	next.Tags = types.ListNull(types.StringType)
	updated, d := l.update(next, created)
	mustSucceed(t, d)

	body := f.calls("PATCH", `/crd`)[0].body
	if body["running"] != false || body["description"] != "" {
		t.Errorf("PATCH body = %v", body)
	}
	if tags, ok := body["tags"].([]any); !ok || len(tags) != 0 {
		t.Errorf("PATCH tags = %v, want []", body["tags"])
	}
	if _, ok := body["remove_install_media"]; ok {
		t.Error("PATCH removes media nobody asked to remove")
	}
	if updated.Phase.ValueString() != "Stopped" || updated.Running.ValueBool() || !updated.Description.IsNull() || !updated.Tags.IsNull() {
		t.Errorf("updated = %+v", updated)
	}
}

// Media are removed by an update, never at creation, and never re-attached.
func TestVirtualMachineInstallMedia(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	l := newLifecycle[virtualMachineModel](t, r)
	modify := func(prior *virtualMachineModel, plan virtualMachineModel) *fwresource.ModifyPlanResponse {
		state := tfsdk.State{Schema: l.s, Raw: l.null()}
		if prior != nil {
			state = l.state(*prior)
		}
		resp := &fwresource.ModifyPlanResponse{Plan: l.plan(plan)}
		r.ModifyPlan(t.Context(), fwresource.ModifyPlanRequest{State: state, Plan: l.plan(plan), Config: l.config(plan)}, resp)
		return resp
	}

	plan := virtualMachinePlan(f.fakeAPI)
	plan.ImageKey = types.StringValue("debian-13.iso")
	plan.InstallMediaRemoved = types.BoolValue(true)
	mustFail(t, modify(nil, plan).Diagnostics, "cannot be removed at creation")

	plan.InstallMediaRemoved = types.BoolValue(false)
	mustSucceed(t, modify(nil, plan).Diagnostics)
	created, d := l.create(plan)
	mustSucceed(t, d)
	if created.InstallMedia.ValueString() != "Attached" || created.InstallMediaRemoved.ValueBool() {
		t.Fatalf("created = %+v", created)
	}

	next := created
	next.InstallMediaRemoved = types.BoolValue(true)
	mustSucceed(t, modify(&created, next).Diagnostics)
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	if f.calls("PATCH", `/crd`)[0].body["remove_install_media"] != true {
		t.Error("PATCH did not remove the media")
	}
	if updated.InstallMedia.ValueString() != "Removed" || !updated.InstallMediaRemoved.ValueBool() {
		t.Errorf("updated = %+v", updated)
	}

	back := updated
	back.InstallMediaRemoved = types.BoolValue(false)
	mustFail(t, modify(&updated, back).Diagnostics, "cannot be re-attached")

	replaced := updated
	replaced.CPU = types.StringValue("4")
	mustFail(t, modify(&updated, replaced).Diagnostics, "cannot be removed at creation")
}

func TestVirtualMachineFailedPhaseNamesTheCause(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	f.failWith = "image import failed: 404 from the image source"
	_, d := newLifecycle[virtualMachineModel](t, r).create(virtualMachinePlan(f.fakeAPI))
	mustFail(t, d, "image import failed")
}

// A deleted machine drops from state whether the console answers 404 or, for
// an id it can no longer resolve, 403; a 403 for a machine still listed stays an error.
func TestVirtualMachineImportAndDrift(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	l := newLifecycle[virtualMachineModel](t, r)
	created, d := l.create(virtualMachinePlan(f.fakeAPI))
	mustSucceed(t, d)

	imported, d := l.importAndRead(created.ID.ValueString())
	mustSucceed(t, d)
	if imported.Name.ValueString() != "web" || imported.ImageKey.ValueString() != "ubuntu-24.04.qcow2" ||
		imported.Env.ValueString() != f.env.String() || imported.CPU.ValueString() != "2" {
		t.Errorf("imported = %+v", imported)
	}

	f.deniedGet = true
	_, removed, d := l.read(created)
	mustFail(t, d, "virtual_machine:read")
	if removed {
		t.Error("a machine still listed was dropped on a 403")
	}

	f.gone = true
	_, removed, d = l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a machine the console denies and no longer lists stayed in state")
	}

	f.deniedGet = false
	_, removed, d = l.read(created)
	mustSucceed(t, d)
	if !removed {
		t.Error("a machine deleted outside Terraform stayed in state")
	}
}

func TestVirtualMachineDelete(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	l := newLifecycle[virtualMachineModel](t, r)
	created, d := l.create(virtualMachinePlan(f.fakeAPI))
	mustSucceed(t, d)
	mustSucceed(t, l.destroy(created))
	if len(f.calls("DELETE", `/virtual-machines/[^/]+/crd`)) != 1 {
		t.Error("the CRD was not deleted")
	}
}

func TestVirtualMachineFeatureDisabled(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("POST", `/virtual-machines/crd`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": vmFeatureDenied}
	})
	_, d := newLifecycle[virtualMachineModel](t, &virtualMachineResource{p: pd}).create(virtualMachinePlan(f))
	mustFail(t, d, "virtual_machines_enabled")
}

func TestVirtualMachineDataSources(t *testing.T) {
	f, r := newFakeVirtualMachine(t)
	created, d := newLifecycle[virtualMachineModel](t, r).create(virtualMachinePlan(f.fakeAPI))
	mustSucceed(t, d)

	got, d := readDataSource(t, &virtualMachineDataSource{p: r.p}, virtualMachineDataSourceModel{
		Env: types.StringValue(f.env.String()), Name: types.StringValue("web"), Tags: types.ListNull(types.StringType),
	})
	mustSucceed(t, d)
	if got.ID != created.ID || got.Phase.ValueString() != "Running" || got.Memory.ValueString() != "4Gi" {
		t.Errorf("data source = %+v", got)
	}
	_, d = readDataSource(t, &virtualMachineDataSource{p: r.p}, virtualMachineDataSourceModel{
		Env: types.StringValue(f.env.String()), Name: types.StringValue("absent"), Tags: types.ListNull(types.StringType),
	})
	mustFail(t, d, "no virtual machine named absent")

	images, d := readDataSource(t, &virtualMachineImagesDataSource{p: r.p}, virtualMachineImagesModel{
		Env: types.StringValue(f.env.String()), Images: types.ListNull(virtualMachineImageType),
	})
	mustSucceed(t, d)
	var list []virtualMachineImageModel
	mustSucceed(t, images.Images.ElementsAs(t.Context(), &list, false))
	if len(list) != 2 || list[1].SourceRef.ValueString() != "platform" || list[1].Key.ValueString() != "debian-13.iso" ||
		list[1].Kind.ValueString() != "installer" {
		t.Errorf("images = %+v", list)
	}
}

func TestVirtualMachineImagesFeatureDisabled(t *testing.T) {
	f, pd := newFakeAPI(t)
	f.on("GET", `/virtual-machines/image-catalog`, func(string, []byte) (int, any) {
		return 403, map[string]string{"message": vmFeatureDenied}
	})
	_, d := readDataSource(t, &virtualMachineImagesDataSource{p: pd}, virtualMachineImagesModel{
		Env: types.StringValue(f.env.String()), Images: types.ListNull(virtualMachineImageType),
	})
	mustFail(t, d, "virtual_machines_enabled")
}
