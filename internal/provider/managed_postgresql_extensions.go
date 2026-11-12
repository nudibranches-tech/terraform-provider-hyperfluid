// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// managed_postgresql_extensions.go — the `extensions` set of a managed
// PostgreSQL (ADR-0054 in the monorepo). The platform owns a catalog of
// extensions; a tenant declares names from it, and the operator installs them
// through CloudNativePG. The console validates the names, and the operator
// checks them again, so this file only shapes the request and reads it back.
//
// Two rules from the platform shape the attribute:
//   - The declared list is replaced as a whole, and an extension left out of
//     it stays installed (reported `unmanaged`). Dropping one is an explicit
//     `ensure = "absent"` entry, because DROP EXTENSION takes its types,
//     columns and indexes with it.
//   - A null `extensions` means Terraform does not manage the list at all, so
//     a database whose extensions are managed elsewhere (the console, hfctl) is
//     never rewritten by an apply that does not mention them.

// extensionModel is one declared extension.
type extensionModel struct {
	Name    types.String `tfsdk:"name"`
	Ensure  types.String `tfsdk:"ensure"`
	Version types.String `tfsdk:"version"`
}

func extensionObjectType() basetypes.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":    types.StringType,
		"ensure":  types.StringType,
		"version": types.StringType,
	}}
}

// extensionStatusModel is one extension as the operator last observed it.
type extensionStatusModel struct {
	Name             types.String `tfsdk:"name"`
	Phase            types.String `tfsdk:"phase"`
	InstalledVersion types.String `tfsdk:"installed_version"`
	AvailableVersion types.String `tfsdk:"available_version"`
	Message          types.String `tfsdk:"message"`
}

func extensionStatusObjectType() basetypes.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":              types.StringType,
		"phase":             types.StringType,
		"installed_version": types.StringType,
		"available_version": types.StringType,
		"message":           types.StringType,
	}}
}

func extensionsSchema() schema.SetNestedAttribute {
	return schema.SetNestedAttribute{
		Optional: true,
		MarkdownDescription: "Extensions to enable from the platform catalog. When set, it is the complete " +
			"declared list. An extension removed from it stays installed (reported `unmanaged`); drop it with " +
			"`ensure = \"absent\"`. Leaving the attribute out does not manage extensions at all. Some extensions " +
			"(`pg_stat_statements`, `pgaudit`) restart the cluster, and PostGIS extensions need `engine = \"postgis\"`. " +
			"Import does not recover it.",
		Validators: []validator.Set{uniqueExtensionNames{}},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Extension name from the platform catalog, e.g. `postgis`, `postgres_fdw`, `pg_stat_statements`.",
					Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				},
				"ensure": schema.StringAttribute{
					Optional: true,
					MarkdownDescription: "`present` (the default) or `absent`. `absent` drops the extension; PostgreSQL " +
						"refuses while anything depends on it.",
					Validators: []validator.String{stringvalidator.OneOf(
						string(console.ExtensionEnsurePresent), string(console.ExtensionEnsureAbsent))},
				},
				"version": schema.StringAttribute{
					Optional:            true,
					MarkdownDescription: "Version to install or update to. Defaults to the version the cluster's image ships.",
					Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				},
			},
		},
	}
}

func extensionStatusSchema() schema.ListNestedAttribute {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	return schema.ListNestedAttribute{
		Computed: true,
		MarkdownDescription: "Extensions the operator reports on this cluster, declared or not. Null when the " +
			"console does not report extensions.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name":              str("Extension name."),
				"phase":             str("`installing`, `installed`, `failed`, `absent` or `unmanaged` (installed but not declared)."),
				"installed_version": str("Version installed in the database right now."),
				"available_version": str("Default version the running image ships."),
				"message":           str("Why the extension failed."),
			},
		},
	}
}

// uniqueExtensionNames refuses two entries for the same extension: the
// platform keeps one declaration per name, so the second would silently win.
type uniqueExtensionNames struct{}

func (uniqueExtensionNames) Description(context.Context) string {
	return "each extension name appears once"
}

func (v uniqueExtensionNames) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (uniqueExtensionNames) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var entries []extensionModel
	resp.Diagnostics.Append(req.ConfigValue.ElementsAs(ctx, &entries, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Name.IsNull() || e.Name.IsUnknown() {
			continue
		}
		name := e.Name.ValueString()
		if seen[name] {
			resp.Diagnostics.AddAttributeError(req.Path, "Duplicate extension",
				fmt.Sprintf("extension %q is declared more than once", name))
			return
		}
		seen[name] = true
	}
}

// extensionRequestsFrom turns the configured set into the API's declared list,
// sorted by name so the request is stable. ok is false when the set is null or
// unknown: Terraform does not manage the list.
func extensionRequestsFrom(ctx context.Context, set types.Set) (items []console.ExtensionRequestItem, ok bool, d diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, false, d
	}
	var entries []extensionModel
	d.Append(set.ElementsAs(ctx, &entries, false)...)
	if d.HasError() {
		return nil, false, d
	}
	items = make([]console.ExtensionRequestItem, 0, len(entries))
	for _, e := range entries {
		item := console.ExtensionRequestItem{Name: e.Name.ValueString()}
		if !e.Ensure.IsNull() && !e.Ensure.IsUnknown() {
			ensure := console.ExtensionEnsure(e.Ensure.ValueString())
			item.Ensure = &ensure
		}
		if !e.Version.IsNull() && !e.Version.IsUnknown() {
			item.Version = e.Version.ValueStringPointer()
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, true, d
}

// createExtensionNames returns the names the create request can carry, or
// ok=false when the list says more than names: the create body only enables
// extensions at the image's version, so a pinned version or an `absent` entry
// is applied with a follow-up PUT instead.
func createExtensionNames(items []console.ExtensionRequestItem) (names []string, ok bool) {
	names = make([]string, 0, len(items))
	for _, i := range items {
		if i.Version != nil || (i.Ensure != nil && *i.Ensure != console.ExtensionEnsurePresent) {
			return nil, false
		}
		names = append(names, i.Name)
	}
	return names, true
}

// extensionsFromDeclared reads the declared list back into the attribute.
// prior is the value the read replaces: a null prior stays null (the list is
// not managed here), and an `ensure` the configuration left out stays null
// although the platform reports it as `present`.
func extensionsFromDeclared(ctx context.Context, resp *console.ManagedPostgresqlExtensionsResponse, prior types.Set) (types.Set, diag.Diagnostics) {
	var d diag.Diagnostics
	if prior.IsNull() || prior.IsUnknown() || resp == nil {
		return prior, d
	}
	var priorEntries []extensionModel
	d.Append(prior.ElementsAs(ctx, &priorEntries, false)...)
	if d.HasError() {
		return prior, d
	}
	priorByName := make(map[string]extensionModel, len(priorEntries))
	for _, e := range priorEntries {
		priorByName[e.Name.ValueString()] = e
	}

	out := []extensionModel{}
	for _, item := range resp.Extensions {
		if item.Declared == nil {
			continue
		}
		ensure := types.StringValue(string(item.Declared.Ensure))
		if p, had := priorByName[item.Name]; item.Declared.Ensure == console.ExtensionEnsurePresent && (!had || p.Ensure.IsNull()) {
			ensure = types.StringNull()
		}
		out = append(out, extensionModel{
			Name:    types.StringValue(item.Name),
			Ensure:  ensure,
			Version: optString(item.Declared.Version),
		})
	}
	set, dd := types.SetValueFrom(ctx, extensionObjectType(), out)
	d.Append(dd...)
	return set, d
}

// extensionStatusFrom lists every extension the operator reports, catalog
// order first and then anything outside the catalog.
func extensionStatusFrom(ctx context.Context, resp *console.ManagedPostgresqlExtensionsResponse) (types.List, diag.Diagnostics) {
	if resp == nil {
		return types.ListNull(extensionStatusObjectType()), nil
	}
	out := []extensionStatusModel{}
	add := func(s console.ExtensionStatus) {
		out = append(out, extensionStatusModel{
			Name:             types.StringValue(s.Name),
			Phase:            types.StringValue(string(s.Phase)),
			InstalledVersion: optString(s.InstalledVersion),
			AvailableVersion: optString(s.AvailableVersion),
			Message:          optString(s.Message),
		})
	}
	for _, item := range resp.Extensions {
		if item.Status != nil {
			add(*item.Status)
		}
	}
	for _, s := range resp.Uncatalogued {
		add(s)
	}
	return types.ListValueFrom(ctx, extensionStatusObjectType(), out)
}

// extensionsSettled reports whether every declared extension reached the
// phase it asks for, or the error of the ones that failed.
func extensionsSettled(resp *console.ManagedPostgresqlExtensionsResponse, want []console.ExtensionRequestItem) (bool, error) {
	observed := map[string]console.ExtensionStatus{}
	for _, item := range resp.Extensions {
		if item.Status != nil {
			observed[item.Name] = *item.Status
		}
	}
	for _, s := range resp.Uncatalogued {
		observed[s.Name] = s
	}

	var failures []string
	settled := true
	for _, w := range want {
		target := console.ExtensionPhaseInstalled
		if w.Ensure != nil && *w.Ensure == console.ExtensionEnsureAbsent {
			target = console.ExtensionPhaseAbsent
		}
		s, seen := observed[w.Name]
		switch {
		case !seen:
			settled = false
		case s.Phase == console.ExtensionPhaseFailed:
			msg := "failed"
			if s.Message != nil && *s.Message != "" {
				msg = *s.Message
			}
			failures = append(failures, fmt.Sprintf("%s: %s", w.Name, msg))
		case s.Phase != target:
			settled = false
		}
	}
	if len(failures) > 0 {
		return false, errors.New(strings.Join(failures, "; "))
	}
	return settled, nil
}

// waitExtensions polls until every declared extension is installed (or
// absent, as asked), and fails as soon as one is reported failed.
func (r *managedPostgresqlResource) waitExtensions(ctx context.Context, id string, want []console.ExtensionRequestItem) error {
	if len(want) == 0 {
		return nil
	}
	_, err := waitForReady(ctx, managedPostgresqlWaitTimeout, func() (*console.ManagedPostgresqlExtensionsResponse, bool, error) {
		resp, err := r.p.API.GetManagedPostgresqlExtensions(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		ok, err := extensionsSettled(resp, want)
		return resp, ok, err
	})
	return err
}

// readExtensions fetches the extensions view. A console that predates the
// endpoint answers 404 for a cluster the caller just read: that is "no
// extensions to report" unless the configuration manages them.
func (r *managedPostgresqlResource) readExtensions(ctx context.Context, id string, managed bool) (*console.ManagedPostgresqlExtensionsResponse, error) {
	resp, err := r.p.API.GetManagedPostgresqlExtensions(ctx, r.p.OrgID, id)
	if errors.Is(err, client.ErrNotFound) {
		if managed {
			return nil, errors.New("this console does not support managed PostgreSQL extensions; remove `extensions` or upgrade the platform")
		}
		return nil, nil
	}
	return resp, err
}
