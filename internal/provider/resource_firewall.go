package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// ============================================================ odinex_firewall

// NewFirewallResource returns the odinex_firewall resource.
func NewFirewallResource() resource.Resource { return &firewallResource{} }

type firewallResource struct{ c *client.Odinex }

type firewallModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Labels  types.Map    `tfsdk:"labels"`
	Rule    types.Set    `tfsdk:"rule"`
	ApplyTo types.Set    `tfsdk:"apply_to"`
}

type firewallRuleModel struct {
	Direction      types.String `tfsdk:"direction"`
	Protocol       types.String `tfsdk:"protocol"`
	Port           types.String `tfsdk:"port"`
	SourceIPs      types.Set    `tfsdk:"source_ips"`
	DestinationIPs types.Set    `tfsdk:"destination_ips"`
	Description    types.String `tfsdk:"description"`
}

type firewallApplyToModel struct {
	Server        types.Int64  `tfsdk:"server"`
	LabelSelector types.String `tfsdk:"label_selector"`
}

var firewallRuleAttrTypes = map[string]attr.Type{
	"direction":       types.StringType,
	"protocol":        types.StringType,
	"port":            types.StringType,
	"source_ips":      types.SetType{ElemType: types.StringType},
	"destination_ips": types.SetType{ElemType: types.StringType},
	"description":     types.StringType,
}

var firewallApplyToAttrTypes = map[string]attr.Type{
	"server":         types.Int64Type,
	"label_selector": types.StringType,
}

func (r *firewallResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall"
}

func (r *firewallResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a firewall. Rules and targets (`apply_to`) are updated in place. " +
			"Use either `apply_to` blocks or `odinex_firewall_attachment`, not both.",
		Attributes: map[string]schema.Attribute{
			"id":     idAttribute(),
			"name":   schema.StringAttribute{Required: true, MarkdownDescription: "Name of the firewall."},
			"labels": labelsAttribute(),
		},
		Blocks: map[string]schema.Block{
			"rule": schema.SetNestedBlock{
				MarkdownDescription: "A firewall rule.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"direction": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "`in` or `out`.",
							Validators:          []validator.String{stringvalidator.OneOf("in", "out")},
						},
						"protocol": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "`tcp`, `udp`, `icmp`, `esp` or `gre`.",
							Validators:          []validator.String{stringvalidator.OneOf("tcp", "udp", "icmp", "esp", "gre")},
						},
						"port": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Port (`80`) or range (`1000-2000`); required for `tcp` and `udp`.",
						},
						"source_ips": schema.SetAttribute{
							ElementType:         types.StringType,
							Optional:            true,
							MarkdownDescription: "Source CIDRs; required for `in` rules.",
						},
						"destination_ips": schema.SetAttribute{
							ElementType:         types.StringType,
							Optional:            true,
							MarkdownDescription: "Destination CIDRs; required for `out` rules.",
						},
						"description": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Description of the rule.",
						},
					},
				},
			},
			"apply_to": schema.SetNestedBlock{
				MarkdownDescription: "Servers or label selectors the firewall applies to. Targets are only tracked while at least one `apply_to` block is configured " +
					"(and are not imported); without blocks, targets managed by `odinex_firewall_attachment` are left alone.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"server": schema.Int64Attribute{
							Optional:            true,
							MarkdownDescription: "ID of a server.",
						},
						"label_selector": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Label selector, e.g. `role=web`.",
						},
					},
				},
			},
		},
	}
}

func (r *firewallResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

// firewallRulesFromSet converts the rule blocks to API rules.
func firewallRulesFromSet(ctx context.Context, set types.Set) ([]client.FirewallRule, diag.Diagnostics) {
	out := []client.FirewallRule{}
	if set.IsNull() || set.IsUnknown() {
		return out, nil
	}
	var rules []firewallRuleModel
	diags := set.ElementsAs(ctx, &rules, false)
	for _, rm := range rules {
		fr := client.FirewallRule{
			Direction:   client.FirewallRuleDirection(rm.Direction.ValueString()),
			Protocol:    client.FirewallRuleProtocol(rm.Protocol.ValueString()),
			Port:        optString(rm.Port),
			Description: optString(rm.Description),
		}
		if src, d := stringsFrom(ctx, rm.SourceIPs); len(src) > 0 {
			fr.SourceIps = &src
			diags.Append(d...)
		}
		if dst, d := stringsFrom(ctx, rm.DestinationIPs); len(dst) > 0 {
			fr.DestinationIps = &dst
			diags.Append(d...)
		}
		out = append(out, fr)
	}
	return out, diags
}

// firewallRulesToSet converts API rules to rule blocks. Empty lists and
// strings become null so that they match omitted arguments.
func firewallRulesToSet(rules []client.FirewallRule) types.Set {
	elems := make([]attr.Value, 0, len(rules))
	for _, fr := range rules {
		ips := func(p *[]string) types.Set {
			if p == nil || len(*p) == 0 {
				return types.SetNull(types.StringType)
			}
			return stringSetValue(*p)
		}
		str := func(p *string) types.String {
			if p == nil || *p == "" {
				return types.StringNull()
			}
			return types.StringValue(*p)
		}
		elems = append(elems, types.ObjectValueMust(firewallRuleAttrTypes, map[string]attr.Value{
			"direction":       types.StringValue(string(fr.Direction)),
			"protocol":        types.StringValue(string(fr.Protocol)),
			"port":            str(fr.Port),
			"source_ips":      ips(fr.SourceIps),
			"destination_ips": ips(fr.DestinationIps),
			"description":     str(fr.Description),
		}))
	}
	return types.SetValueMust(types.ObjectType{AttrTypes: firewallRuleAttrTypes}, elems)
}

func firewallTargetsFromSet(ctx context.Context, set types.Set) ([]client.FirewallTarget, diag.Diagnostics) {
	out := []client.FirewallTarget{}
	if set.IsNull() || set.IsUnknown() {
		return out, nil
	}
	var ts []firewallApplyToModel
	diags := set.ElementsAs(ctx, &ts, false)
	for _, t := range ts {
		switch {
		case !t.Server.IsNull() && !t.LabelSelector.IsNull():
			diags.AddError("Invalid apply_to block", "Set either `server` or `label_selector` in an apply_to block, not both.")
		case !t.Server.IsNull():
			out = append(out, client.FirewallTarget{Type: "server", Server: ptr(t.Server.ValueInt64())})
		case !t.LabelSelector.IsNull():
			out = append(out, client.FirewallTarget{Type: "label_selector", LabelSelector: ptr(t.LabelSelector.ValueString())})
		default:
			diags.AddError("Invalid apply_to block", "Set `server` or `label_selector` in an apply_to block.")
		}
	}
	return out, diags
}

func firewallTargetKey(t client.FirewallTarget) string {
	if t.Type == "server" && t.Server != nil {
		return "server:" + strconv.FormatInt(*t.Server, 10)
	}
	return "label_selector:" + derefStr(t.LabelSelector)
}

func firewallAppliedTargets(fw *client.Firewall) []client.FirewallTarget {
	out := make([]client.FirewallTarget, 0, len(fw.AppliedTo))
	for _, a := range fw.AppliedTo {
		out = append(out, client.FirewallTarget{Type: client.FirewallTargetType(a.Type), Server: a.Server, LabelSelector: a.LabelSelector})
	}
	return out
}

func firewallTargetsToSet(ts []client.FirewallTarget) types.Set {
	elems := make([]attr.Value, 0, len(ts))
	for _, t := range ts {
		v := map[string]attr.Value{"server": types.Int64Null(), "label_selector": types.StringNull()}
		if t.Type == "server" {
			v["server"] = int64PtrValue(t.Server)
		} else {
			v["label_selector"] = strPtrValue(t.LabelSelector)
		}
		elems = append(elems, types.ObjectValueMust(firewallApplyToAttrTypes, v))
	}
	return types.SetValueMust(types.ObjectType{AttrTypes: firewallApplyToAttrTypes}, elems)
}

// diffFirewallTargets returns the targets to add and remove.
func diffFirewallTargets(have, want []client.FirewallTarget) (add, remove []client.FirewallTarget) {
	h := map[string]bool{}
	for _, t := range have {
		h[firewallTargetKey(t)] = true
	}
	w := map[string]bool{}
	for _, t := range want {
		k := firewallTargetKey(t)
		w[k] = true
		if !h[k] {
			add = append(add, t)
		}
	}
	for _, t := range have {
		if !w[firewallTargetKey(t)] {
			remove = append(remove, t)
		}
	}
	return add, remove
}

func applyFirewallTargets(ctx context.Context, c *client.Odinex, id int64, add, remove []client.FirewallTarget) error {
	if len(remove) > 0 {
		res, err := client.Result(c.RemoveFirewallWithResponse(ctx, id, client.RemoveFirewallJSONRequestBody{RemoveFrom: remove}))
		if err == nil {
			err = c.WaitForAction(ctx, &res.JSON201.Action)
		}
		if err != nil {
			return fmt.Errorf("removing firewall from resources: %w", err)
		}
	}
	if len(add) > 0 {
		res, err := client.Result(c.ApplyFirewallWithResponse(ctx, id, client.ApplyFirewallJSONRequestBody{ApplyTo: add}))
		if err == nil {
			err = c.WaitForAction(ctx, &res.JSON201.Action)
		}
		if err != nil {
			return fmt.Errorf("applying firewall to resources: %w", err)
		}
	}
	return nil
}

func (r *firewallResource) toModel(fw *client.Firewall, m *firewallModel) {
	m.ID = types.StringValue(client.FormatID(fw.Id))
	m.Name = types.StringValue(fw.Name)
	m.Labels = labelsToMap(fw.Labels)
	m.Rule = firewallRulesToSet(fw.Rules)
	if len(fw.Rules) == 0 {
		m.Rule = types.SetNull(types.ObjectType{AttrTypes: firewallRuleAttrTypes})
	}
	// apply_to is only tracked when configured, so that
	// odinex_firewall_attachment can manage the targets instead.
	if !m.ApplyTo.IsNull() {
		m.ApplyTo = firewallTargetsToSet(firewallAppliedTargets(fw))
	}
}

func (r *firewallResource) get(ctx context.Context, id int64) (*client.Firewall, error) {
	g, err := client.Result(r.c.GetFirewallWithResponse(ctx, id))
	if err != nil {
		return nil, err
	}
	return &g.JSON200.Firewall, nil
}

func (r *firewallResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan firewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	rules, d := firewallRulesFromSet(ctx, plan.Rule)
	resp.Diagnostics.Append(d...)
	targets, d := firewallTargetsFromSet(ctx, plan.ApplyTo)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.CreateFirewallJSONRequestBody{
		Name:   plan.Name.ValueString(),
		Labels: &labels,
		Rules:  &rules,
	}
	if len(targets) > 0 {
		body.ApplyTo = &targets
	}
	res, err := client.Result(r.c.CreateFirewallWithResponse(ctx, body))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating firewall", err)
		return
	}
	id := res.JSON201.Firewall.Id
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	if err := r.c.WaitForAction(ctx, res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for firewall creation", err)
		return
	}
	fw, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading firewall", err)
		return
	}
	r.toModel(fw, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *firewallResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid firewall ID", err.Error())
		return
	}
	fw, err := r.get(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading firewall", err)
		return
	}
	r.toModel(fw, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state firewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	if !plan.Name.Equal(state.Name) || !plan.Labels.Equal(state.Labels) {
		labels, d := labelsFromMap(ctx, plan.Labels)
		resp.Diagnostics.Append(d...)
		if _, err := client.Result(r.c.UpdateFirewallWithResponse(ctx, id, client.UpdateFirewallJSONRequestBody{
			Name:   ptr(plan.Name.ValueString()),
			Labels: &labels,
		})); err != nil {
			apiErr(&resp.Diagnostics, "Error updating firewall", err)
			return
		}
	}
	if !plan.Rule.Equal(state.Rule) {
		rules, d := firewallRulesFromSet(ctx, plan.Rule)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		res, err := client.Result(r.c.SetFirewallRulesWithResponse(ctx, id, client.SetFirewallRulesJSONRequestBody{Rules: rules}))
		if err == nil {
			err = r.c.WaitForAction(ctx, &res.JSON201.Action)
		}
		if err != nil {
			apiErr(&resp.Diagnostics, "Error setting firewall rules", err)
			return
		}
	}
	if !plan.ApplyTo.Equal(state.ApplyTo) {
		want, d := firewallTargetsFromSet(ctx, plan.ApplyTo)
		resp.Diagnostics.Append(d...)
		have, d := firewallTargetsFromSet(ctx, state.ApplyTo)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		add, remove := diffFirewallTargets(have, want)
		if err := applyFirewallTargets(ctx, r.c, id, add, remove); err != nil {
			apiErr(&resp.Diagnostics, "Error updating firewall targets", err)
			return
		}
	}
	fw, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading firewall", err)
		return
	}
	r.toModel(fw, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *firewallResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	fw, err := r.get(ctx, id)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading firewall", err)
		return
	}
	// A firewall still applied to resources cannot be deleted.
	if targets := firewallAppliedTargets(fw); len(targets) > 0 {
		if err := applyFirewallTargets(ctx, r.c, id, nil, targets); err != nil {
			apiErr(&resp.Diagnostics, "Error removing firewall from resources", err)
			return
		}
	}
	if _, err := client.Result(r.c.DeleteFirewallWithResponse(ctx, id)); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error deleting firewall", err)
	}
}

func (r *firewallResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListFirewallsWithResponse(ctx, &client.ListFirewallsParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing firewalls", lerr)
			return
		}
		for _, f := range res.JSON200.Firewalls {
			if f.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(f.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("Firewall not found", fmt.Sprintf("No firewall with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ============================================================ odinex_firewall_attachment

// NewFirewallAttachmentResource returns the odinex_firewall_attachment resource.
func NewFirewallAttachmentResource() resource.Resource { return &firewallAttachmentResource{} }

type firewallAttachmentResource struct{ c *client.Odinex }

type firewallAttachmentModel struct {
	ID             types.String `tfsdk:"id"`
	FirewallID     types.Int64  `tfsdk:"firewall_id"`
	ServerIDs      types.Set    `tfsdk:"server_ids"`
	LabelSelectors types.Set    `tfsdk:"label_selectors"`
}

func (r *firewallAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_attachment"
}

func (r *firewallAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Applies a firewall to servers and label selectors. The ID is the firewall ID. " +
			"Do not use `apply_to` blocks on the same `odinex_firewall`.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute(),
			"firewall_id": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "ID of the firewall.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"server_ids": schema.SetAttribute{
				ElementType:         types.Int64Type,
				Optional:            true,
				MarkdownDescription: "IDs of servers to apply the firewall to.",
			},
			"label_selectors": schema.SetAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Label selectors whose servers get the firewall.",
			},
		},
	}
}

func (r *firewallAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func (m *firewallAttachmentModel) targets(ctx context.Context) ([]client.FirewallTarget, diag.Diagnostics) {
	var diags diag.Diagnostics
	ids, d := int64sFrom(ctx, m.ServerIDs)
	diags.Append(d...)
	sels, d := stringsFrom(ctx, m.LabelSelectors)
	diags.Append(d...)
	out := []client.FirewallTarget{}
	for _, id := range ids {
		out = append(out, client.FirewallTarget{Type: "server", Server: ptr(id)})
	}
	for _, s := range sels {
		out = append(out, client.FirewallTarget{Type: "label_selector", LabelSelector: ptr(s)})
	}
	return out, diags
}

func (r *firewallAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan firewallAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	want, d := plan.targets(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(want) == 0 {
		resp.Diagnostics.AddError("Nothing to attach", "Set `server_ids` or `label_selectors`.")
		return
	}
	fid := plan.FirewallID.ValueInt64()
	g, err := client.Result(r.c.GetFirewallWithResponse(ctx, fid))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading firewall", err)
		return
	}
	add, _ := diffFirewallTargets(firewallAppliedTargets(&g.JSON200.Firewall), want)
	if err := applyFirewallTargets(ctx, r.c, fid, add, nil); err != nil {
		apiErr(&resp.Diagnostics, "Error attaching firewall", err)
		return
	}
	plan.ID = types.StringValue(client.FormatID(fid))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *firewallAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state firewallAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fid, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid firewall attachment ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetFirewallWithResponse(ctx, fid))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading firewall", err)
		return
	}
	var ids []int64
	var sels []string
	for _, t := range firewallAppliedTargets(&g.JSON200.Firewall) {
		if t.Type == "server" && t.Server != nil {
			ids = append(ids, *t.Server)
		} else if t.LabelSelector != nil {
			sels = append(sels, *t.LabelSelector)
		}
	}
	if len(ids) == 0 && len(sels) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}
	state.FirewallID = types.Int64Value(fid)
	state.ServerIDs = types.SetNull(types.Int64Type)
	if len(ids) > 0 {
		state.ServerIDs = int64SetValue(ids)
	}
	state.LabelSelectors = types.SetNull(types.StringType)
	if len(sels) > 0 {
		state.LabelSelectors = stringSetValue(sels)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state firewallAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	want, d := plan.targets(ctx)
	resp.Diagnostics.Append(d...)
	have, d := state.targets(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	add, remove := diffFirewallTargets(have, want)
	if err := applyFirewallTargets(ctx, r.c, plan.FirewallID.ValueInt64(), add, remove); err != nil {
		apiErr(&resp.Diagnostics, "Error updating firewall attachment", err)
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *firewallAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state firewallAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	have, d := state.targets(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() || len(have) == 0 {
		return
	}
	err := applyFirewallTargets(ctx, r.c, state.FirewallID.ValueInt64(), nil, have)
	if err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error detaching firewall", err)
	}
}

func (r *firewallAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", "Import a firewall attachment by the firewall ID.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
