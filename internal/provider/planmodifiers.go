package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// useStateUnlessChanged keeps the prior state value of a computed attribute
// in the plan, unless one of the listed sibling attributes changes (then the
// value stays unknown because the API may change it).
type useStateUnlessChanged struct{ deps []string }

func useStateUnless(deps ...string) useStateUnlessChanged { return useStateUnlessChanged{deps: deps} }

func (m useStateUnlessChanged) Description(context.Context) string {
	return "Uses the prior state value unless one of " + strings.Join(m.deps, ", ") + " changes."
}

func (m useStateUnlessChanged) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m useStateUnlessChanged) keep(ctx context.Context, state tfsdk.State, plan tfsdk.Plan, stateVal, planVal, cfgVal attr.Value) bool {
	if state.Raw.IsNull() || plan.Raw.IsNull() || stateVal.IsNull() || !planVal.IsUnknown() || cfgVal.IsUnknown() {
		return false
	}
	for _, dep := range m.deps {
		var sv, pv attr.Value
		if d := state.GetAttribute(ctx, path.Root(dep), &sv); d.HasError() {
			return false
		}
		if d := plan.GetAttribute(ctx, path.Root(dep), &pv); d.HasError() {
			return false
		}
		if pv == nil || sv == nil || pv.IsUnknown() || !pv.Equal(sv) {
			return false
		}
	}
	return true
}

func (m useStateUnlessChanged) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if m.keep(ctx, req.State, req.Plan, req.StateValue, req.PlanValue, req.ConfigValue) {
		resp.PlanValue = req.StateValue
	}
}

func (m useStateUnlessChanged) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if m.keep(ctx, req.State, req.Plan, req.StateValue, req.PlanValue, req.ConfigValue) {
		resp.PlanValue = req.StateValue
	}
}

func (m useStateUnlessChanged) PlanModifySet(ctx context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	if m.keep(ctx, req.State, req.Plan, req.StateValue, req.PlanValue, req.ConfigValue) {
		resp.PlanValue = req.StateValue
	}
}
