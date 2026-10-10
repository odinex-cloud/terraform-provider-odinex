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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// ============================================================ odinex_nat_gateway

// NewNatGatewayResource returns the odinex_nat_gateway resource.
func NewNatGatewayResource() resource.Resource { return &natGatewayResource{} }

type natGatewayResource struct{ c *client.Odinex }

type natGatewayModel struct {
	ID        types.String   `tfsdk:"id"`
	Name      types.String   `tfsdk:"name"`
	NetworkID types.Int64    `tfsdk:"network_id"`
	Location  types.String   `tfsdk:"location"`
	PublicIP  types.String   `tfsdk:"public_ip"`
	PrivateIP types.String   `tfsdk:"private_ip"`
	Status    types.String   `tfsdk:"status"`
	Labels    types.Map      `tfsdk:"labels"`
	Timeouts  timeouts.Value `tfsdk:"timeouts"`
}

func (r *natGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nat_gateway"
}

func (r *natGatewayResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the NAT gateway of a private network (one per network). Servers without public IPs in the network " +
			"reach the internet from the gateway's public IPv4; the gateway takes the first host of the network (its `gateway`).",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute(),
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the NAT gateway.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"network_id": requiredIDAttr("ID of the network. Changing it creates a new NAT gateway."),
			"location": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Location of the NAT gateway (the network's location).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"public_ip": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Public IPv4 address the network's traffic leaves from.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"private_ip": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Address of the gateway inside the network (the default gateway of its servers).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Status of the NAT gateway (`creating`, `available`, `deleting`).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"labels": labelsAttribute(),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *natGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func natGatewayToModel(g *client.NatGateway, m *natGatewayModel) {
	m.ID = types.StringValue(client.FormatID(g.Id))
	m.Name = types.StringValue(g.Name)
	m.NetworkID = types.Int64Value(g.Network)
	m.Location = types.StringValue(g.Location)
	m.PublicIP = types.StringValue(derefStr(g.PublicIp))
	m.PrivateIP = types.StringValue(g.PrivateIp)
	m.Status = types.StringValue(string(g.Status))
	m.Labels = labelsToMap(g.Labels)
}

func (r *natGatewayResource) get(ctx context.Context, id int64) (*client.NatGateway, error) {
	g, err := client.Result(r.c.GetNatGatewayWithResponse(ctx, id))
	if err != nil {
		return nil, err
	}
	return &g.JSON200.NatGateway, nil
}

func (r *natGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan natGatewayModel
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
	res, err := client.Result(r.c.CreateNatGatewayWithResponse(ctx, client.CreateNatGatewayJSONRequestBody{
		Name:    optString(plan.Name),
		Network: client.FormatID(plan.NetworkID.ValueInt64()),
		Labels:  &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating NAT gateway", err)
		return
	}
	id := res.JSON201.NatGateway.Id
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	if err := r.c.WaitForAction(ctx, &res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for NAT gateway creation", err)
		return
	}
	g, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading NAT gateway", err)
		return
	}
	natGatewayToModel(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *natGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid NAT gateway ID", err.Error())
		return
	}
	g, err := r.get(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading NAT gateway", err)
		return
	}
	natGatewayToModel(g, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *natGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state natGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	body := client.UpdateNatGatewayJSONRequestBody{Labels: &labels}
	if !plan.Name.Equal(state.Name) && !plan.Name.IsUnknown() {
		body.Name = ptr(plan.Name.ValueString())
	}
	res, err := client.Result(r.c.UpdateNatGatewayWithResponse(ctx, id, body))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error updating NAT gateway", err)
		return
	}
	natGatewayToModel(&res.JSON200.NatGateway, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *natGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())
	res, err := client.Result(r.c.DeleteNatGatewayWithResponse(ctx, id))
	if client.IsNotFound(err) {
		return
	}
	if err == nil {
		err = r.c.WaitForAction(ctx, &res.JSON200.Action)
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error deleting NAT gateway", err)
	}
}

func (r *natGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListNatGatewaysWithResponse(ctx, &client.ListNatGatewaysParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing NAT gateways", lerr)
			return
		}
		for _, g := range res.JSON200.NatGateways {
			if g.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(g.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("NAT gateway not found", fmt.Sprintf("No NAT gateway with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ============================================================ odinex_elastic_ip

// NewElasticIPResource returns the odinex_elastic_ip resource.
func NewElasticIPResource() resource.Resource { return &elasticIPResource{} }

type elasticIPResource struct{ c *client.Odinex }

type elasticIPModel struct {
	ID               types.String   `tfsdk:"id"`
	Name             types.String   `tfsdk:"name"`
	Location         types.String   `tfsdk:"location"`
	ServerID         types.Int64    `tfsdk:"server_id"`
	IPAddress        types.String   `tfsdk:"ip_address"`
	Labels           types.Map      `tfsdk:"labels"`
	DeleteProtection types.Bool     `tfsdk:"delete_protection"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (r *elasticIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_elastic_ip"
}

func (r *elasticIPResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Elastic IP: a public IPv4 address NATed to a server without public IPs " +
			"(its primary network needs an `odinex_nat_gateway`). Nothing is configured inside the guest. " +
			"Assign it with `server_id` or with `odinex_elastic_ip_assignment` (not both).",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute(),
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the Elastic IP.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"location": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Location of the Elastic IP. Required unless `server_id` is set.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"server_id": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Server the IP is assigned to. The server must have no public IPs.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"ip_address": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The IP address.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"labels":            labelsAttribute(),
			"delete_protection": deleteProtectionAttribute(),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *elasticIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func elasticIPToModel(e *client.ElasticIp, m *elasticIPModel) {
	m.ID = types.StringValue(client.FormatID(e.Id))
	m.Name = types.StringValue(e.Name)
	m.Location = types.StringValue(e.Location)
	m.ServerID = int64PtrValue(e.Server)
	m.IPAddress = types.StringValue(derefStr(e.Ip))
	m.Labels = labelsToMap(e.Labels)
	m.DeleteProtection = types.BoolValue(e.Protection.Delete)
}

func (r *elasticIPResource) get(ctx context.Context, id int64) (*client.ElasticIp, error) {
	g, err := client.Result(r.c.GetElasticIpWithResponse(ctx, id))
	if err != nil {
		return nil, err
	}
	return &g.JSON200.ElasticIp, nil
}

func (r *elasticIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan elasticIPModel
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
	if optString(plan.Location) == nil && optInt64(plan.ServerID) == nil {
		resp.Diagnostics.AddAttributeError(path.Root("location"), "Missing location", "Set `location` or `server_id`.")
		return
	}
	res, err := client.Result(r.c.CreateElasticIpWithResponse(ctx, client.CreateElasticIpJSONRequestBody{
		Name:     optString(plan.Name),
		Location: optString(plan.Location),
		Server:   optInt64(plan.ServerID),
		Labels:   &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating Elastic IP", err)
		return
	}
	id := res.JSON201.ElasticIp.Id
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	if err := r.c.WaitForAction(ctx, &res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for Elastic IP creation", err)
		return
	}
	if plan.DeleteProtection.ValueBool() {
		if _, err := client.Result(r.c.UpdateElasticIpWithResponse(ctx, id, client.UpdateElasticIpJSONRequestBody{
			Protection: &struct {
				Delete *bool `json:"delete,omitempty"`
			}{Delete: ptr(true)},
		})); err != nil {
			apiErr(&resp.Diagnostics, "Error enabling delete protection", err)
			return
		}
	}
	e, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading Elastic IP", err)
		return
	}
	elasticIPToModel(e, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *elasticIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state elasticIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid Elastic IP ID", err.Error())
		return
	}
	e, err := r.get(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading Elastic IP", err)
		return
	}
	elasticIPToModel(e, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *elasticIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state elasticIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())
	body := client.UpdateElasticIpJSONRequestBody{
		Labels: &labels,
		Protection: &struct {
			Delete *bool `json:"delete,omitempty"`
		}{Delete: ptr(plan.DeleteProtection.ValueBool())},
	}
	if !plan.Name.Equal(state.Name) && !plan.Name.IsUnknown() {
		body.Name = ptr(plan.Name.ValueString())
	}
	if _, err := client.Result(r.c.UpdateElasticIpWithResponse(ctx, id, body)); err != nil {
		apiErr(&resp.Diagnostics, "Error updating Elastic IP", err)
		return
	}
	if !plan.ServerID.IsUnknown() && !plan.ServerID.Equal(state.ServerID) {
		var err error
		if plan.ServerID.IsNull() {
			err = unassignElasticIP(ctx, r.c, id)
		} else {
			err = assignElasticIP(ctx, r.c, id, plan.ServerID.ValueInt64())
		}
		if err != nil {
			apiErr(&resp.Diagnostics, "Error changing Elastic IP assignment", err)
			return
		}
	}
	e, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading Elastic IP", err)
		return
	}
	elasticIPToModel(e, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *elasticIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state elasticIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())
	res, err := client.Result(r.c.DeleteElasticIpWithResponse(ctx, id))
	if client.IsNotFound(err) {
		return
	}
	if err == nil {
		err = r.c.WaitForAction(ctx, &res.JSON200.Action)
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error deleting Elastic IP", err)
	}
}

func (r *elasticIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListElasticIpsWithResponse(ctx, &client.ListElasticIpsParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing Elastic IPs", lerr)
			return
		}
		for _, e := range res.JSON200.ElasticIps {
			if e.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(e.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("Elastic IP not found", fmt.Sprintf("No Elastic IP with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
