package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// ============================================================ odinex_network

// NewNetworkResource returns the odinex_network resource.
func NewNetworkResource() resource.Resource { return &networkResource{} }

type networkResource struct{ c *client.Odinex }

type networkModel struct {
	ID               types.String   `tfsdk:"id"`
	Name             types.String   `tfsdk:"name"`
	IPRange          types.String   `tfsdk:"ip_range"`
	Location         types.String   `tfsdk:"location"`
	Labels           types.Map      `tfsdk:"labels"`
	DeleteProtection types.Bool     `tfsdk:"delete_protection"`
	Gateway          types.String   `tfsdk:"gateway"`
	NatGatewayID     types.Int64    `tfsdk:"nat_gateway_id"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a private network.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name of the network."},
			"ip_range": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Private IPv4 range in CIDR notation, /16 to /28 (e.g. `10.0.0.0/16`). Changing it recreates the network.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"location": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Location of the network, e.g. `fra1`. Changing it recreates the network.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"labels":            labelsAttribute(),
			"delete_protection": deleteProtectionAttribute(),
			"gateway": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Address kept for a NAT gateway: the first host of `ip_range`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"nat_gateway_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "ID of the network's NAT gateway (see `odinex_nat_gateway`), if any.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *networkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func networkToModel(n *client.Network, m *networkModel) {
	m.ID = types.StringValue(client.FormatID(n.Id))
	m.Name = types.StringValue(n.Name)
	m.IPRange = types.StringValue(n.IpRange)
	m.Location = types.StringValue(n.Location)
	m.Labels = labelsToMap(n.Labels)
	m.DeleteProtection = types.BoolValue(n.Protection.Delete)
	m.Gateway = types.StringValue(n.Gateway)
	m.NatGatewayID = int64PtrValue(n.NatGateway)
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := client.Result(r.c.CreateNetworkWithResponse(ctx, client.CreateNetworkJSONRequestBody{
		Name:     plan.Name.ValueString(),
		IpRange:  plan.IPRange.ValueString(),
		Location: plan.Location.ValueString(),
		Labels:   &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating network", err)
		return
	}
	id := res.JSON201.Network.Id
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	if err := r.c.WaitForAction(ctx, res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for network creation", err)
		return
	}
	if plan.DeleteProtection.ValueBool() {
		if _, err := client.Result(r.c.UpdateNetworkWithResponse(ctx, id, client.UpdateNetworkJSONRequestBody{
			Protection: &struct {
				Delete *bool `json:"delete,omitempty"`
			}{Delete: ptr(true)},
		})); err != nil {
			apiErr(&resp.Diagnostics, "Error enabling delete protection", err)
			return
		}
	}
	g, err := client.Result(r.c.GetNetworkWithResponse(ctx, id))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading network", err)
		return
	}
	networkToModel(&g.JSON200.Network, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid network ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetNetworkWithResponse(ctx, id))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading network", err)
		return
	}
	networkToModel(&g.JSON200.Network, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	res, err := client.Result(r.c.UpdateNetworkWithResponse(ctx, id, client.UpdateNetworkJSONRequestBody{
		Name:   ptr(plan.Name.ValueString()),
		Labels: &labels,
		Protection: &struct {
			Delete *bool `json:"delete,omitempty"`
		}{Delete: ptr(plan.DeleteProtection.ValueBool())},
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error updating network", err)
		return
	}
	// A NAT gateway created or deleted in the same run shows up on the next
	// refresh; the planned value must be kept until then.
	natGW := plan.NatGatewayID
	networkToModel(&res.JSON200.Network, &plan)
	if !natGW.IsUnknown() {
		plan.NatGatewayID = natGW
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())
	// Servers detached in the same run may still be leaving the network.
	var res *client.DeleteNetworkResponse
	var err error
	for attempt := 0; ; attempt++ {
		res, err = client.Result(r.c.DeleteNetworkWithResponse(ctx, id))
		if !client.IsCode(err, "resource_in_use") || attempt >= 30 {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			continue
		}
		break
	}
	if client.IsNotFound(err) {
		return
	}
	if err == nil {
		err = r.c.WaitForAction(ctx, &res.JSON200.Action)
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error deleting network", err)
	}
}

func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListNetworksWithResponse(ctx, &client.ListNetworksParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing networks", lerr)
			return
		}
		for _, n := range res.JSON200.Networks {
			if n.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(n.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("Network not found", fmt.Sprintf("No network with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ============================================================ odinex_placement_group

// NewPlacementGroupResource returns the odinex_placement_group resource.
func NewPlacementGroupResource() resource.Resource { return &placementGroupResource{} }

type placementGroupResource struct{ c *client.Odinex }

type placementGroupModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	Location types.String `tfsdk:"location"`
	Labels   types.Map    `tfsdk:"labels"`
	Servers  types.Set    `tfsdk:"servers"`
}

func (r *placementGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_placement_group"
}

func (r *placementGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a placement group (servers of a `spread` group run on different hosts).",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name of the placement group."},
			"type": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("spread"),
				MarkdownDescription: "Type of the placement group. Only `spread` is supported.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"location": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Location of the placement group, e.g. `fra1`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"labels": labelsAttribute(),
			"servers": schema.SetAttribute{
				ElementType:         types.Int64Type,
				Computed:            true,
				MarkdownDescription: "IDs of the servers in the group.",
				PlanModifiers:       []planmodifier.Set{useStateUnless()},
			},
		},
	}
}

func (r *placementGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func placementGroupToModel(p *client.PlacementGroup, m *placementGroupModel) {
	m.ID = types.StringValue(client.FormatID(p.Id))
	m.Name = types.StringValue(p.Name)
	m.Type = types.StringValue(string(p.Type))
	m.Location = types.StringValue(p.Location)
	m.Labels = labelsToMap(p.Labels)
	m.Servers = int64SetValue(p.Servers)
}

func (r *placementGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan placementGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	t := client.CreatePlacementGroupJSONBodyType(plan.Type.ValueString())
	res, err := client.Result(r.c.CreatePlacementGroupWithResponse(ctx, client.CreatePlacementGroupJSONRequestBody{
		Name:     plan.Name.ValueString(),
		Location: plan.Location.ValueString(),
		Type:     &t,
		Labels:   &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating placement group", err)
		return
	}
	if err := r.c.WaitForAction(ctx, res.JSON201.Action); err != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(res.JSON201.PlacementGroup.Id))...)
		apiErr(&resp.Diagnostics, "Error waiting for placement group creation", err)
		return
	}
	placementGroupToModel(&res.JSON201.PlacementGroup, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *placementGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state placementGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid placement group ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetPlacementGroupWithResponse(ctx, id))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading placement group", err)
		return
	}
	placementGroupToModel(&g.JSON200.PlacementGroup, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *placementGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state placementGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	res, err := client.Result(r.c.UpdatePlacementGroupWithResponse(ctx, id, client.UpdatePlacementGroupJSONRequestBody{
		Name:   ptr(plan.Name.ValueString()),
		Labels: &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error updating placement group", err)
		return
	}
	placementGroupToModel(&res.JSON200.PlacementGroup, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *placementGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state placementGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	if _, err := client.Result(r.c.DeletePlacementGroupWithResponse(ctx, id)); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error deleting placement group", err)
	}
}

func (r *placementGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListPlacementGroupsWithResponse(ctx, &client.ListPlacementGroupsParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing placement groups", lerr)
			return
		}
		for _, p := range res.JSON200.PlacementGroups {
			if p.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(p.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("Placement group not found", fmt.Sprintf("No placement group with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
