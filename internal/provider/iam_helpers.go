// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	uuidPattern          = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	permissionKeyPattern = regexp.MustCompile(`^[a-z0-9_]+:[a-z0-9_]+$`)
)

func isUUID() validator.String {
	return stringvalidator.RegexMatches(uuidPattern, "must be a UUID")
}

func uuidFromString(s string) (openapi_types.UUID, error) {
	return uuid.Parse(s)
}

// iamStateNote is appended to the description of every attribute that stores a
// credential in Terraform state.
const iamStateNote = " Stored in Terraform state, so keep state encrypted."

// permissionCatalog indexes the platform's permission catalog both ways. Keys
// (`secret:read`) are the same on every installation, ids are not, which is
// why configurations name permissions by key.
type permissionCatalog struct {
	idByKey map[string]openapi_types.UUID
	keyByID map[string]string
}

func loadPermissionCatalog(ctx context.Context, api *client.Client) (*permissionCatalog, error) {
	entries, err := api.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}
	return newPermissionCatalog(entries), nil
}

func newPermissionCatalog(entries []console.PermissionCatalogEntry) *permissionCatalog {
	c := &permissionCatalog{
		idByKey: make(map[string]openapi_types.UUID, len(entries)),
		keyByID: make(map[string]string, len(entries)),
	}
	for _, e := range entries {
		c.idByKey[e.Key] = e.Id
		c.keyByID[e.Id.String()] = e.Key
	}
	return c
}

// ids resolves permission keys, naming every key the catalog does not know.
func (c *permissionCatalog) ids(keys []string) ([]openapi_types.UUID, error) {
	out := make([]openapi_types.UUID, 0, len(keys))
	var unknown []string
	for _, k := range keys {
		id, ok := c.idByKey[k]
		if !ok {
			unknown = append(unknown, k)
			continue
		}
		out = append(out, id)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown permission %s; the hyperfluid_permissions data source lists the catalog",
			strings.Join(quoteAll(unknown), ", "))
	}
	return out, nil
}

// key returns the key of a permission id, or the id itself when the catalog
// no longer lists it, so state still records what the grant holds.
func (c *permissionCatalog) key(id openapi_types.UUID) string {
	if k, ok := c.keyByID[id.String()]; ok {
		return k
	}
	return id.String()
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// nonNil turns a missing list into an empty one, so it converts to an empty
// set rather than a null one.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
