// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeIAM is the slice of the console API the IAM resources touch: groups,
// members, grants and role bindings, the permission catalog and role bundles,
// service accounts and S3 keys, held in memory.
type fakeIAM struct {
	t   *testing.T
	org uuid.UUID
	env uuid.UUID

	mu          sync.Mutex
	catalog     []console.PermissionCatalogEntry
	roleBundles map[string][]uuid.UUID // role id → permission ids
	systemRoles map[string]bool
	roles       map[string]*console.Role
	userRoles   map[string][]string // user id → org-wide role ids
	groups      map[string]*console.Group
	members     map[string][]console.GroupMember // group id → members
	grants      []console.Grant
	accounts    map[string]*fakeAccount // client id → account
	keys        []console.S3KeyListEntry
	requests    []iamRequest

	// forbid answers 403 with this body to every request whose "METHOD path"
	// starts with the key.
	forbid map[string]string
}

type fakeAccount struct {
	row        console.ServiceAccount
	attributes []string
}

type iamRequest struct {
	method, path, query string
	body                map[string]any
}

func newFakeIAM(t *testing.T) (*fakeIAM, *providerData) {
	t.Helper()
	f := &fakeIAM{
		t: t, org: uuid.New(), env: uuid.New(),
		roleBundles: map[string][]uuid.UUID{}, systemRoles: map[string]bool{},
		roles: map[string]*console.Role{}, userRoles: map[string][]string{},
		groups: map[string]*console.Group{}, members: map[string][]console.GroupMember{},
		accounts: map[string]*fakeAccount{}, forbid: map[string]string{},
	}
	for _, key := range []string{"secret:read", "secret:list", "secret:update", "bucket:read_objects", "bucket:list_objects"} {
		rt, action, _ := strings.Cut(key, ":")
		f.catalog = append(f.catalog, console.PermissionCatalogEntry{Id: uuid.New(), Key: key, ResourceType: rt, Action: action})
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	api, err := client.New(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return f, &providerData{API: api, OrgID: f.org.String()}
}

func (f *fakeIAM) permissionID(key string) uuid.UUID {
	for _, e := range f.catalog {
		if e.Key == key {
			return e.Id
		}
	}
	f.t.Fatalf("no permission %q in the fake catalog", key)
	return uuid.Nil
}

// calls returns the recorded requests of that method whose path ends with suffix.
func (f *fakeIAM) calls(method, suffix string) []iamRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []iamRequest
	for _, r := range f.requests {
		if r.method == method && strings.HasSuffix(r.path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeIAM) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("%s %s: body is not JSON: %v", r.Method, r.URL.Path, err)
		}
	}
	f.requests = append(f.requests, iamRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: body})
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	decode := func(v any) bool {
		if err := json.Unmarshal(raw, v); err != nil {
			reply(400, map[string]string{"message": err.Error()})
			return false
		}
		return true
	}
	for prefix, msg := range f.forbid {
		if strings.HasPrefix(r.Method+" "+r.URL.Path, prefix) {
			reply(403, map[string]string{"message": msg})
			return
		}
	}

	seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// api/v1/...
	if len(seg) < 3 {
		reply(404, nil)
		return
	}
	seg = seg[2:]
	switch {
	case seg[0] == "permissions" && r.Method == http.MethodGet:
		reply(200, f.catalog)
	case seg[0] == "roles" && len(seg) <= 2:
		f.serveRoleCRUD(r, seg[1:], reply, decode)
	case seg[0] == "roles" && len(seg) == 3:
		f.serveRole(r, seg[1], reply, decode)
	case seg[0] == "organizations" && len(seg) >= 5 && seg[2] == "users" && seg[4] == "roles":
		f.serveUserRoles(r, seg[3], seg[5:], reply)
	case seg[0] == "organizations" && len(seg) >= 3 && seg[2] == "groups":
		f.serveGroups(r, seg[3:], reply, decode)
	case seg[0] == "organizations" && len(seg) >= 3 && seg[2] == "grants" && r.Method == http.MethodDelete:
		f.deleteGrant(seg[3], reply)
	case seg[0] == "organizations" && len(seg) >= 5 && seg[2] == "users" && seg[4] == "grants":
		f.serveGrants(r, client.PrincipalUser, seg[3], reply, decode)
	case seg[0] == "organizations" && len(seg) >= 5 && seg[2] == "service-accounts":
		f.serveAccountByID(r, seg[3], seg[4:], reply, decode)
	case seg[0] == "orgs" && len(seg) == 3 && seg[2] == "service-accounts":
		f.listAccounts(r, reply)
	case seg[0] == "harbors":
		f.serveS3(r, seg[2:], reply, decode)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		reply(404, map[string]string{"message": "unexpected"})
	}
}

type replyFn func(int, any)
type decodeFn func(any) bool

func (f *fakeIAM) serveRole(r *http.Request, role string, reply replyFn, decode decodeFn) {
	if _, err := uuid.Parse(role); err != nil || f.roleBundles[role] == nil && !f.systemRoles[role] {
		reply(404, map[string]string{"message": "Role not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := []console.PermissionCatalogEntry{}
		for _, e := range f.catalog {
			for _, id := range f.roleBundles[role] {
				if id == e.Id {
					out = append(out, e)
				}
			}
		}
		reply(200, out)
	case http.MethodPut:
		if f.systemRoles[role] {
			reply(403, map[string]string{"message": "System role permission bundles are managed by the platform and cannot be edited"})
			return
		}
		var req console.SetRolePermissionsRequestBody
		if !decode(&req) {
			return
		}
		f.roleBundles[role] = append([]uuid.UUID{}, req.PermissionIds...)
		reply(204, nil)
	}
}

// addRole adds a role the way the platform holds one; a system role refuses
// edits.
func (f *fakeIAM) addRole(name string, system bool) *console.Role {
	role := &console.Role{Id: uuid.New(), Name: name, OrganizationId: f.org, IsSystem: system, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.roles[role.Id.String()] = role
	f.roleBundles[role.Id.String()] = []uuid.UUID{}
	f.systemRoles[role.Id.String()] = system
	return role
}

func (f *fakeIAM) serveRoleCRUD(r *http.Request, rest []string, reply replyFn, decode decodeFn) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			name := strings.ToLower(r.URL.Query().Get("name"))
			out := []console.Role{}
			for _, role := range f.roles {
				if strings.Contains(strings.ToLower(role.Name), name) {
					out = append(out, *role)
				}
			}
			reply(200, console.ListRolesResponseData{Roles: out})
		case http.MethodPost:
			var req console.CreateRoleRequestBody
			if !decode(&req) {
				return
			}
			for _, role := range f.roles {
				if strings.EqualFold(role.Name, req.Name) {
					reply(409, map[string]string{"message": "A role with this name already exists"})
					return
				}
			}
			role := f.addRole(req.Name, false)
			role.Description, role.Color = req.Description, req.Color
			reply(201, role)
		}
		return
	}
	role, ok := f.roles[rest[0]]
	if !ok {
		reply(404, map[string]string{"message": "Role not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(200, role)
	case http.MethodPut:
		if role.IsSystem {
			reply(403, map[string]string{"message": "System roles cannot be updated"})
			return
		}
		var req console.UpdateRoleRequestBody
		if !decode(&req) {
			return
		}
		role.Name, role.Description, role.Color = req.Name, req.Description, req.Color
		reply(200, role)
	case http.MethodDelete:
		if role.IsSystem {
			reply(403, map[string]string{"message": "System roles cannot be deleted"})
			return
		}
		delete(f.roles, rest[0])
		delete(f.roleBundles, rest[0])
		f.dropGrants(func(g console.Grant) bool { return g.RoleId != nil && g.RoleId.String() == rest[0] })
		reply(204, nil)
	}
}

// serveUserRoles mirrors the dual path of a user's roles: an organization-wide
// role is a row of the user's roles plus a mirror grant, a scoped one a grant
// only.
func (f *fakeIAM) serveUserRoles(r *http.Request, user string, rest []string, reply replyFn) {
	if len(rest) == 0 {
		ids, known := f.userRoles[user]
		if !known {
			reply(404, map[string]string{"message": "User not found"})
			return
		}
		out := []console.Role{}
		for _, id := range ids {
			if role, ok := f.roles[id]; ok {
				out = append(out, *role)
			}
		}
		reply(200, out)
		return
	}
	role := rest[0]
	if _, ok := f.roles[role]; !ok {
		reply(404, map[string]string{"message": "Role not found"})
		return
	}
	scoped := r.URL.Query().Get("scope_path") != ""
	if !scoped {
		kept := []string{}
		for _, id := range f.userRoles[user] {
			if id != role {
				kept = append(kept, id)
			}
		}
		if r.Method == http.MethodPost {
			kept = append(kept, role)
		}
		f.userRoles[user] = kept
	}
	f.serveRoleBinding(r, client.PrincipalUser, user, role, reply)
}

func (f *fakeIAM) serveGroups(r *http.Request, rest []string, reply replyFn, decode decodeFn) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			out := []console.Group{}
			for _, g := range f.groups {
				out = append(out, *g)
			}
			reply(200, out)
		case http.MethodPost:
			var req console.CreateGroupRequestBody
			if !decode(&req) {
				return
			}
			for _, g := range f.groups {
				if g.Name == req.Name {
					reply(500, map[string]string{"message": "Authorization operation failed"})
					return
				}
			}
			g := &console.Group{Id: uuid.New(), OrganizationId: f.org, Name: req.Name, Description: req.Description}
			f.groups[g.Id.String()] = g
			reply(201, g)
		}
		return
	}
	g, ok := f.groups[rest[0]]
	if !ok {
		reply(404, map[string]string{"message": "Group not found: " + rest[0]})
		return
	}
	switch {
	case len(rest) == 1 && r.Method == http.MethodGet:
		g.MemberCount = int64(len(f.members[rest[0]]))
		reply(200, g)
	case len(rest) == 1 && r.Method == http.MethodPut:
		var req console.UpdateGroupRequestBody
		if !decode(&req) {
			return
		}
		g.Name, g.Description = req.Name, req.Description
		reply(200, g)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		delete(f.groups, rest[0])
		delete(f.members, rest[0])
		f.dropGrants(func(gr console.Grant) bool { return gr.PrincipalId.String() == rest[0] })
		reply(204, nil)
	case rest[1] == "members" && len(rest) == 2 && r.Method == http.MethodGet:
		reply(200, append([]console.GroupMember{}, f.members[rest[0]]...))
	case rest[1] == "members" && len(rest) == 2 && r.Method == http.MethodPost:
		var req console.AddGroupMemberRequestBody
		if !decode(&req) {
			return
		}
		for _, m := range f.members[rest[0]] {
			if m.PrincipalType == req.PrincipalType && m.PrincipalId == req.PrincipalId {
				reply(204, nil)
				return
			}
		}
		f.members[rest[0]] = append(f.members[rest[0]], console.GroupMember{
			PrincipalType: req.PrincipalType, PrincipalId: req.PrincipalId, PrincipalName: "member-" + req.PrincipalId.String()[:8],
		})
		reply(204, nil)
	case rest[1] == "members" && len(rest) == 4 && r.Method == http.MethodDelete:
		kept := []console.GroupMember{}
		for _, m := range f.members[rest[0]] {
			if m.PrincipalType != rest[2] || m.PrincipalId.String() != rest[3] {
				kept = append(kept, m)
			}
		}
		f.members[rest[0]] = kept
		reply(204, nil)
	case rest[1] == "grants":
		f.serveGrants(r, client.PrincipalGroup, rest[0], reply, decode)
	case rest[1] == "roles" && len(rest) == 3:
		f.serveRoleBinding(r, client.PrincipalGroup, rest[0], rest[2], reply)
	default:
		f.t.Errorf("unexpected group request %s %s", r.Method, r.URL.Path)
		reply(404, nil)
	}
}

func (f *fakeIAM) serveGrants(r *http.Request, ptype, pid string, reply replyFn, decode decodeFn) {
	switch r.Method {
	case http.MethodGet:
		out := []console.Grant{}
		for _, g := range f.grants {
			if g.PrincipalType == ptype && g.PrincipalId.String() == pid {
				out = append(out, g)
			}
		}
		reply(200, out)
	case http.MethodPost:
		var req console.CreateGrantRequestBody
		if !decode(&req) {
			return
		}
		effect := "allow"
		if req.Effect != nil {
			effect = string(*req.Effect)
		}
		g := console.Grant{
			Id: uuid.New(), PrincipalType: ptype, PrincipalId: uuid.MustParse(pid), OrganizationId: f.org,
			PermissionId: &req.PermissionId, ScopePath: req.ScopePath, ResourceUuid: req.ResourceUuid, Effect: effect,
		}
		f.grants = append(f.grants, g)
		reply(201, g)
	}
}

func (f *fakeIAM) serveRoleBinding(r *http.Request, ptype, pid, role string, reply replyFn) {
	q := r.URL.Query()
	var scope *string
	if s := q.Get("scope_path"); s != "" {
		scope = &s
	}
	var anchor *uuid.UUID
	if s := q.Get("resource_uuid"); s != "" {
		u := uuid.MustParse(s)
		anchor = &u
	}
	same := func(g console.Grant) bool {
		return g.PrincipalType == ptype && g.PrincipalId.String() == pid && g.RoleId != nil &&
			g.RoleId.String() == role && g.PermissionId == nil && strOrEmpty(g.ScopePath) == strOrEmpty(scope)
	}
	switch r.Method {
	case http.MethodPost:
		for _, g := range f.grants {
			if same(g) && uuidStr(g.ResourceUuid) == uuidStr(anchor) {
				reply(204, nil)
				return
			}
		}
		rid := uuid.MustParse(role)
		f.grants = append(f.grants, console.Grant{
			Id: uuid.New(), PrincipalType: ptype, PrincipalId: uuid.MustParse(pid), OrganizationId: f.org,
			RoleId: &rid, ScopePath: scope, ResourceUuid: anchor, Effect: "allow",
		})
		reply(204, nil)
	case http.MethodDelete:
		f.dropGrants(same)
		reply(204, nil)
	}
}

func (f *fakeIAM) deleteGrant(id string, reply replyFn) {
	before := len(f.grants)
	f.dropGrants(func(g console.Grant) bool { return g.Id.String() == id })
	if len(f.grants) == before {
		reply(404, map[string]string{"message": "Grant not found: " + id})
		return
	}
	reply(204, nil)
}

func (f *fakeIAM) dropGrants(drop func(console.Grant) bool) {
	kept := []console.Grant{}
	for _, g := range f.grants {
		if !drop(g) {
			kept = append(kept, g)
		}
	}
	f.grants = kept
}

func (f *fakeIAM) accountByID(id string) *fakeAccount {
	for _, a := range f.accounts {
		if a.row.Id.String() == id {
			return a
		}
	}
	return nil
}

// addAccount adds a confidential account, as created in the console.
func (f *fakeIAM) addAccount(clientID, name string) *fakeAccount {
	a := &fakeAccount{row: console.ServiceAccount{
		Id: uuid.New(), OrganizationId: f.org, IamSaClientId: clientID, Name: name, Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	f.accounts[clientID] = a
	return a
}

func (f *fakeIAM) serveAccountByID(r *http.Request, id string, rest []string, reply replyFn, decode decodeFn) {
	switch rest[0] {
	case "grants":
		f.serveGrants(r, client.PrincipalServiceAccount, id, reply, decode)
	case "roles":
		f.serveRoleBinding(r, client.PrincipalServiceAccount, id, rest[1], reply)
	case "attributes":
		a := f.accountByID(id)
		if a == nil {
			reply(404, map[string]string{"message": "Service account not found in this organization"})
			return
		}
		if r.Method == http.MethodGet {
			reply(200, console.ServiceAccountAttributesResponse{Attributes: append([]string{}, a.attributes...)})
			return
		}
		var req console.SetServiceAccountAttributesRequest
		if !decode(&req) {
			return
		}
		a.attributes = req.Attributes
		reply(204, nil)
	default:
		f.t.Errorf("unexpected service account request %s %s", r.Method, r.URL.Path)
		reply(404, nil)
	}
}

func (f *fakeIAM) listAccounts(r *http.Request, reply replyFn) {
	name := strings.ToLower(r.URL.Query().Get("name"))
	out := []console.ServiceAccount{}
	for _, a := range f.accounts {
		if name == "" || strings.Contains(strings.ToLower(a.row.Name), name) {
			out = append(out, a.row)
		}
	}
	reply(200, console.ListServiceAccountsResponseData{ServiceAccounts: out})
}

func (f *fakeIAM) serveS3(r *http.Request, rest []string, reply replyFn, decode decodeFn) {
	switch {
	case rest[0] == "s3-keys" && len(rest) == 1 && r.Method == http.MethodGet:
		reply(200, append([]console.S3KeyListEntry{}, f.keys...))
	case rest[0] == "s3-keys" && len(rest) == 1 && r.Method == http.MethodPost:
		var req console.IssueS3KeyRequest
		if !decode(&req) {
			return
		}
		a := f.accountByID(req.ServiceAccountId.String())
		if a == nil {
			reply(404, map[string]string{"message": "No such service account in this organization."})
			return
		}
		entry := f.mint(a, req.Label)
		reply(200, console.IssuedS3Key{
			Id: entry.Id, AccessKeyId: entry.AccessKeyId, SecretAccessKey: "s3-secret-" + entry.AccessKeyId,
			Endpoint: "https://s3.example.com", Region: "us-east-1", Tenant: entry.Tenant, KeyEpoch: entry.KeyEpoch,
		})
	case rest[0] == "s3-keys" && len(rest) == 2 && r.Method == http.MethodDelete:
		var target *console.S3KeyListEntry
		for i := range f.keys {
			if f.keys[i].Id.String() == rest[1] && f.keys[i].Active {
				target = &f.keys[i]
			}
		}
		if target == nil {
			reply(404, map[string]string{"message": "No such active key in this organization."})
			return
		}
		now := time.Now()
		also := []string{}
		for i := range f.keys {
			k := &f.keys[i]
			if k.Active && k.Subject == target.Subject && k.KeyEpoch <= target.KeyEpoch {
				if k.Id != target.Id {
					also = append(also, k.AccessKeyId)
				}
				k.Active, k.RevokedAt = false, &now
			}
		}
		reply(200, console.RevokedS3Key{Id: target.Id, RevokedAt: now, AlsoRevoked: also, EffectiveWithinSeconds: 60})
	default:
		f.t.Errorf("unexpected S3 key request %s %s", r.Method, r.URL.Path)
		reply(404, nil)
	}
}

func (f *fakeIAM) mint(a *fakeAccount, label *string) console.S3KeyListEntry {
	epoch := int32(1)
	for _, k := range f.keys {
		if k.Subject == a.row.IamSaClientId && k.KeyEpoch >= epoch {
			epoch = k.KeyEpoch + 1
		}
	}
	id := a.row.Id
	entry := console.S3KeyListEntry{
		Id: uuid.New(), Tenant: "acme-dev", Subject: a.row.IamSaClientId, AccessKeyId: "HFK" + uuid.NewString()[:8],
		KeyEpoch: epoch, Label: label, CreatedAt: time.Now(), Active: true, ServiceAccountId: &id,
		Scopes: []string{"acme-dev/exports"}, Permissions: []string{"bucket:read_objects"},
	}
	f.keys = append(f.keys, entry)
	return entry
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uuidStr(u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}
