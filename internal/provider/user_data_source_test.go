// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func newFakeOrgUsers(t *testing.T, users ...console.UserWithRoles) *providerData {
	t.Helper()
	f, p := newFakeAPI(t)
	f.on("GET", `/organizations/`+f.org.String()+`/users`, func(string, []byte) (int, any) { return 200, users })
	return p
}

func orgUser(email string, roles ...string) console.UserWithRoles {
	u := console.UserWithRoles{Id: uuid.New(), Email: email, FirstName: "Alice", LastName: "Martin", Roles: []console.Role{}}
	for _, name := range roles {
		u.Roles = append(u.Roles, console.Role{Id: uuid.New(), Name: name})
	}
	return u
}

func readUser(t *testing.T, p *providerData, email string) (userDataSourceModel, diag.Diagnostics) {
	t.Helper()
	return readDataSource(t, &userDataSource{p: p}, userDataSourceModel{Email: types.StringValue(email)})
}

func TestUserDataSourceMatch(t *testing.T) {
	alice := orgUser("alice@example.com", "editor", "auditor")
	p := newFakeOrgUsers(t, orgUser("bob@example.com"), alice)

	got, d := readUser(t, p, "alice@example.com")
	mustSucceed(t, d)
	if got.ID.ValueString() != alice.Id.String() || got.FirstName.ValueString() != "Alice" || got.LastName.ValueString() != "Martin" {
		t.Errorf("user = %+v", got)
	}
	if len(got.Roles) != 2 || got.Roles[0].Name.ValueString() != "editor" || got.Roles[1].ID.ValueString() != alice.Roles[1].Id.String() {
		t.Errorf("roles = %+v", got.Roles)
	}
	if got.Email.ValueString() != "alice@example.com" {
		t.Errorf("email = %s, want the configured value", got.Email)
	}
}

func TestUserDataSourceMatchIgnoresCaseAndSpaces(t *testing.T) {
	alice := orgUser("Alice@Example.com")
	p := newFakeOrgUsers(t, alice)

	got, d := readUser(t, p, "  alice@EXAMPLE.com ")
	mustSucceed(t, d)
	if got.ID.ValueString() != alice.Id.String() {
		t.Errorf("id = %s, want %s", got.ID, alice.Id)
	}
	if got.Roles == nil {
		t.Error("a user without roles must report an empty list, not null")
	}
}

func TestUserDataSourceNotFound(t *testing.T) {
	p := newFakeOrgUsers(t, orgUser("bob@example.com"))

	_, d := readUser(t, p, "alice@example.com")
	mustFail(t, d, `no member of the organization has the email "alice@example.com"`)
	mustFail(t, d, "invitation accepted")
}

func TestUserDataSourceDuplicates(t *testing.T) {
	p := newFakeOrgUsers(t, orgUser("alice@example.com"), orgUser("ALICE@example.com"))

	_, d := readUser(t, p, "alice@example.com")
	mustFail(t, d, `2 organization members have the email "alice@example.com"`)
}

func TestUserDataSourceForbidden(t *testing.T) {
	for name, tc := range map[string]struct {
		body map[string]string
		want string
	}{
		"denial naming the permission": {
			body: map[string]string{"message": "Missing permission 'user:read'", "required_permission": "user:read"},
			want: `missing permission "user:read"`,
		},
		"bare refusal": {
			body: map[string]string{"message": "You are not authorized to list the members of this organization"},
			want: `You are not authorized to list the members of this organization (this call requires the "user:read" permission)`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, p := newFakeAPI(t)
			f.on("GET", `/users`, func(string, []byte) (int, any) { return 403, tc.body })

			_, d := readUser(t, p, "alice@example.com")
			mustFail(t, d, "forbidden")
			mustFail(t, d, tc.want)
		})
	}
}
