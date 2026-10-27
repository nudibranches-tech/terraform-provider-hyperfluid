// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func TestGroupMemberLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	group := console.Group{Id: uuid.New(), Name: "ops"}
	f.groups[group.Id.String()] = &group
	user := uuid.NewString()
	l := newLifecycle[groupMemberModel](t, &groupMemberResource{p: pd})

	plan := groupMemberModel{
		ID: types.StringUnknown(), GroupID: types.StringValue(group.Id.String()),
		PrincipalType: types.StringValue("user"), PrincipalID: types.StringValue(user),
		PrincipalName: types.StringUnknown(),
	}
	created, d := l.create(plan)
	mustSucceed(t, d)
	wantID := group.Id.String() + "/user/" + user
	if created.ID.ValueString() != wantID || created.PrincipalName.ValueString() != "member-"+user[:8] {
		t.Fatalf("created = %+v", created)
	}

	imported, d := l.importAndRead(wantID)
	mustSucceed(t, d)
	if imported != created {
		t.Errorf("imported = %+v, want %+v", imported, created)
	}

	mustSucceed(t, l.destroy(created))
	if n := len(f.members[group.Id.String()]); n != 0 {
		t.Fatalf("members after destroy = %d", n)
	}
	if _, removed, d := l.read(created); !removed || d.HasError() {
		t.Errorf("a removed member must leave state (removed=%v, %v)", removed, d)
	}

	// The group itself deleted out of band: the membership is gone with it.
	created, d = l.create(plan)
	mustSucceed(t, d)
	delete(f.groups, group.Id.String())
	if _, removed, d := l.read(created); !removed || d.HasError() {
		t.Errorf("a member of a deleted group must leave state (removed=%v, %v)", removed, d)
	}
	mustSucceed(t, l.destroy(created))
}

func TestGroupMemberImportIDIsValidated(t *testing.T) {
	_, pd := newFakeIAM(t)
	l := newLifecycle[groupMemberModel](t, &groupMemberResource{p: pd})
	_, d := l.importAndRead("only/two")
	mustFail(t, d, "<group_id>/<principal_type>/<principal_id>")
}
