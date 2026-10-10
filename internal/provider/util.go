package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// ---------------------------------------------------------------- schema bits

func idAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "Unique ID of the resource.",
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func labelsAttribute() schema.MapAttribute {
	return schema.MapAttribute{
		ElementType:         types.StringType,
		Optional:            true,
		Computed:            true,
		Default:             mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})),
		MarkdownDescription: "User-defined labels (key/value pairs). Up to 64 pairs; keys and values up to 63 characters.",
	}
}

func deleteProtectionAttribute() schema.BoolAttribute {
	return schema.BoolAttribute{
		Optional:            true,
		Computed:            true,
		Default:             booldefault.StaticBool(false),
		MarkdownDescription: "Prevent the resource from being deleted. Defaults to `false`.",
	}
}

type diagT = diag.Diagnostics

// ---------------------------------------------------------------- conversions

// labelsToMap converts API labels to a Terraform map (never null).
func labelsToMap(l client.Labels) types.Map {
	m := make(map[string]attr.Value, len(l))
	for k, v := range l {
		m[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, m)
}

// labelsFromMap converts a Terraform map to API labels. Null or unknown maps
// become an empty label set.
func labelsFromMap(ctx context.Context, m types.Map) (client.Labels, diag.Diagnostics) {
	out := client.Labels{}
	if m.IsNull() || m.IsUnknown() {
		return out, nil
	}
	var raw map[string]string
	d := m.ElementsAs(ctx, &raw, false)
	for k, v := range raw {
		out[k] = v
	}
	return out, d
}

func stringSetValue(vals []string) types.Set {
	elems := make([]attr.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elems)
}

func stringListValue(vals []string) types.List {
	elems := make([]attr.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

func int64ListValue(vals []int64) types.List {
	elems := make([]attr.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, types.Int64Value(v))
	}
	return types.ListValueMust(types.Int64Type, elems)
}

func int64SetValue(vals []int64) types.Set {
	elems := make([]attr.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, types.Int64Value(v))
	}
	return types.SetValueMust(types.Int64Type, elems)
}

func stringsFrom(ctx context.Context, v interface {
	IsNull() bool
	IsUnknown() bool
	ElementsAs(context.Context, any, bool) diag.Diagnostics
}) ([]string, diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return nil, nil
	}
	var out []string
	d := v.ElementsAs(ctx, &out, false)
	return out, d
}

func int64sFrom(ctx context.Context, v interface {
	IsNull() bool
	IsUnknown() bool
	ElementsAs(context.Context, any, bool) diag.Diagnostics
}) ([]int64, diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return nil, nil
	}
	var out []int64
	d := v.ElementsAs(ctx, &out, false)
	return out, d
}

func strPtrValue(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func int64PtrValue(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

func ptr[T any](v T) *T { return &v }

// optString returns nil for null/unknown/empty strings.
func optString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	s := v.ValueString()
	return &s
}

func optInt64(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	n := v.ValueInt64()
	return &n
}

// ---------------------------------------------------------------- set diff

// diffInt64 returns the elements to add (in want, not in have) and remove.
func diffInt64(have, want []int64) (add, remove []int64) {
	h := map[int64]bool{}
	for _, v := range have {
		h[v] = true
	}
	w := map[int64]bool{}
	for _, v := range want {
		w[v] = true
		if !h[v] {
			add = append(add, v)
		}
	}
	for _, v := range have {
		if !w[v] {
			remove = append(remove, v)
		}
	}
	return add, remove
}

func diffStrings(have, want []string) (add, remove []string) {
	h := map[string]bool{}
	for _, v := range have {
		h[v] = true
	}
	w := map[string]bool{}
	for _, v := range want {
		w[v] = true
		if !h[v] {
			add = append(add, v)
		}
	}
	for _, v := range have {
		if !w[v] {
			remove = append(remove, v)
		}
	}
	return add, remove
}

// ---------------------------------------------------------------- label selectors

// matchLabelSelector evaluates an Odinex/Hetzner style label selector
// (`k=v`, `k==v`, `k!=v`, `k`, `!k`, `k in (a,b)`, `k notin (a,b)`; comma = AND)
// against a label set. It is used where the API has no label_selector
// parameter (images).
func matchLabelSelector(selector string, labels map[string]string) (bool, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return true, nil
	}
	for _, term := range splitSelector(selector) {
		term = strings.TrimSpace(term)
		if term == "" {
			return false, fmt.Errorf("empty term in label selector %q", selector)
		}
		ok, err := matchTerm(term, labels)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// splitSelector splits on commas that are not inside parentheses.
func splitSelector(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func matchTerm(term string, labels map[string]string) (bool, error) {
	if strings.HasPrefix(term, "!") {
		_, has := labels[strings.TrimSpace(term[1:])]
		return !has, nil
	}
	if i := strings.Index(term, "!="); i >= 0 {
		k, v := strings.TrimSpace(term[:i]), strings.TrimSpace(term[i+2:])
		return labels[k] != v, nil
	}
	if i := strings.Index(term, "=="); i >= 0 {
		k, v := strings.TrimSpace(term[:i]), strings.TrimSpace(term[i+2:])
		got, has := labels[k]
		return has && got == v, nil
	}
	if i := strings.Index(term, "="); i >= 0 {
		k, v := strings.TrimSpace(term[:i]), strings.TrimSpace(term[i+1:])
		got, has := labels[k]
		return has && got == v, nil
	}
	for _, op := range []string{" notin ", " in "} {
		if i := strings.Index(term, op); i >= 0 {
			k := strings.TrimSpace(term[:i])
			rest := strings.TrimSpace(term[i+len(op):])
			if !strings.HasPrefix(rest, "(") || !strings.HasSuffix(rest, ")") {
				return false, fmt.Errorf("invalid set expression %q", term)
			}
			set := map[string]bool{}
			for _, v := range strings.Split(rest[1:len(rest)-1], ",") {
				set[strings.TrimSpace(v)] = true
			}
			got, has := labels[k]
			if op == " in " {
				return has && set[got], nil
			}
			return !has || !set[got], nil
		}
	}
	_, has := labels[term]
	return has, nil
}

// ---------------------------------------------------------------- configure helpers

func clientFromResource(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *client.Odinex {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*client.Odinex)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Odinex, got %T", req.ProviderData))
		return nil
	}
	return c
}

func clientFromDataSource(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *client.Odinex {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*client.Odinex)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Odinex, got %T", req.ProviderData))
		return nil
	}
	return c
}

// apiErr adds an error diagnostic for an API or action failure.
func apiErr(d *diag.Diagnostics, summary string, err error) {
	d.AddError(summary, err.Error())
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var objAsOpts = basetypes.ObjectAsOptions{}
