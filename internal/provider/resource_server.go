package provider

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

var (
	_ resource.Resource                   = (*serverResource)(nil)
	_ resource.ResourceWithImportState    = (*serverResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*serverResource)(nil)
	_ resource.ResourceWithValidateConfig = (*serverResource)(nil)
)

// NewServerResource returns the odinex_server resource.
func NewServerResource() resource.Resource { return &serverResource{} }

type serverResource struct{ c *client.Odinex }

type serverModel struct {
	ID                     types.String   `tfsdk:"id"`
	Name                   types.String   `tfsdk:"name"`
	ServerType             types.String   `tfsdk:"server_type"`
	Image                  types.String   `tfsdk:"image"`
	Location               types.String   `tfsdk:"location"`
	SSHKeys                types.List     `tfsdk:"ssh_keys"`
	UserData               types.String   `tfsdk:"user_data"`
	Labels                 types.Map      `tfsdk:"labels"`
	Backups                types.Bool     `tfsdk:"backups"`
	PlacementGroupID       types.Int64    `tfsdk:"placement_group_id"`
	DeleteProtection       types.Bool     `tfsdk:"delete_protection"`
	KeepDisk               types.Bool     `tfsdk:"keep_disk"`
	ShutdownBeforeDeletion types.Bool     `tfsdk:"shutdown_before_deletion"`
	Network                types.Set      `tfsdk:"network"`
	PrimaryNetworkID       types.Int64    `tfsdk:"primary_network_id"`
	FirewallIDs            types.Set      `tfsdk:"firewall_ids"`
	PublicNet              types.Object   `tfsdk:"public_net"`
	IPv4Address            types.String   `tfsdk:"ipv4_address"`
	IPv6Address            types.String   `tfsdk:"ipv6_address"`
	PrimaryIPv4ID          types.Int64    `tfsdk:"primary_ipv4_id"`
	PrimaryIPv6ID          types.Int64    `tfsdk:"primary_ipv6_id"`
	Status                 types.String   `tfsdk:"status"`
	BackupWindow           types.String   `tfsdk:"backup_window"`
	PrimaryDiskSize        types.Int64    `tfsdk:"primary_disk_size"`
	Timeouts               timeouts.Value `tfsdk:"timeouts"`
}

type serverNetworkBlock struct {
	NetworkID types.Int64  `tfsdk:"network_id"`
	IP        types.String `tfsdk:"ip"`
}

var serverNetworkAttrTypes = map[string]attr.Type{
	"network_id": types.Int64Type,
	"ip":         types.StringType,
}

var serverPublicNetAttrTypes = map[string]attr.Type{
	"ipv4_enabled": types.BoolType,
	"ipv6_enabled": types.BoolType,
}

func (r *serverResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server"
}

func (r *serverResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Odinex Cloud server.\n\n" +
			"Changing `server_type` powers the server off, changes the type and powers it on again. " +
			"Changing `image` rebuilds the server (all data on its disk is lost).",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute(),
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the server (hostname-safe: letters, digits, dots and dashes).",
			},
			"server_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name or ID of the server type (e.g. `s2`). Can be changed in place: the server is powered off, resized and powered on.",
			},
			"image": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name or ID of the image (e.g. `ubuntu-24.04`, or a snapshot ID). Changing it rebuilds the server.",
			},
			"location": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Location (region) name, e.g. `fra1`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ssh_keys": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "SSH key names or IDs to install. Changing them recreates the server. Without SSH keys a root password is generated (not stored).",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"user_data": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Cloud-init user data. Changing it recreates the server.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"labels": labelsAttribute(),
			"firewall_ids": schema.SetAttribute{
				ElementType: types.Int64Type,
				Optional:    true,
				MarkdownDescription: "IDs of firewalls applied directly to the server (at most 5 at creation; they are active before the first boot). " +
					"Changes apply or remove the firewalls in place. Only tracked while set; firewalls applied through label selectors are not listed. " +
					"Use either this or `odinex_firewall.apply_to` / `odinex_firewall_attachment` for a given server, not both.",
				Validators: []validator.Set{setvalidator.SizeAtMost(5)},
			},
			"backups": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Enable daily backups (+20 % of the server price). Disabling deletes all existing backups.",
			},
			"placement_group_id": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "ID of the placement group. The API only sets it at creation, so changing it recreates the server.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"delete_protection": deleteProtectionAttribute(),
			"keep_disk": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "When changing `server_type`, keep the current disk size so the server can be downgraded later. Defaults to `false` (the disk is upgraded).",
			},
			"shutdown_before_deletion": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Gracefully shut the server down before deleting it. Defaults to `false`.",
			},
			"primary_network_id": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Network a server without public IPs (`public_net` with both disabled) lives on: it is attached at creation " +
					"and the server reaches the internet through its `odinex_nat_gateway`. Defaults to the only `network` block; " +
					"required when there are several. Its `network` block cannot have a fixed `ip`. Changing it recreates the server.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIfConfigured(), int64planmodifier.UseStateForUnknown()},
			},
			"ipv4_address": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Public IPv4 address (empty when IPv4 is disabled).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ipv6_address": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Public IPv6 address (empty when IPv6 is disabled).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"primary_ipv4_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "ID of the primary IP holding the public IPv4 address (use it with `odinex_rdns.primary_ip_id`).",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"primary_ipv6_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "ID of the primary IP holding the public IPv6 address.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Status of the server (`running`, `off`, …).",
				PlanModifiers:       []planmodifier.String{useStateUnless("server_type", "image")},
			},
			"backup_window": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "UTC hours of the daily backup, e.g. `22-02`; empty when backups are off.",
				PlanModifiers:       []planmodifier.String{useStateUnless("backups")},
			},
			"primary_disk_size": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Size of the primary disk in GB.",
				PlanModifiers:       []planmodifier.Int64{useStateUnless("server_type", "image")},
			},
		},
		Blocks: map[string]schema.Block{
			"network": schema.SetNestedBlock{
				MarkdownDescription: "Private networks to attach the server to. Networks are attached and detached in place. " +
					"Do not combine with `odinex_server_network` for the same server.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"network_id": schema.Int64Attribute{
							Required:            true,
							MarkdownDescription: "ID of the network.",
						},
						"ip": schema.StringAttribute{
							Optional:            true,
							Computed:            true,
							MarkdownDescription: "Fixed IP inside the network; assigned automatically when omitted.",
						},
					},
				},
			},
			"public_net": schema.SingleNestedBlock{
				MarkdownDescription: "Public network configuration. Changing it recreates the server. Disable both for a server without public IPs: " +
					"it needs a network (see `primary_network_id`) and is reachable through an `odinex_elastic_ip` or a load balancer with `network_id`.",
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplaceIf(publicNetChanged, "Changing public_net recreates the server.", "Changing `public_net` recreates the server.")},
				Attributes: map[string]schema.Attribute{
					"ipv4_enabled": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						Default:             booldefault.StaticBool(true),
						MarkdownDescription: "Assign a public IPv4 address. Defaults to `true`.",
					},
					"ipv6_enabled": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						Default:             booldefault.StaticBool(true),
						MarkdownDescription: "Assign a public IPv6 address. Defaults to `true`.",
					},
				},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// publicNetFlags returns the effective ipv4/ipv6 flags of a public_net value
// (a missing block means both enabled).
func publicNetFlags(o types.Object) (bool, bool) {
	v4, v6 := true, true
	if o.IsNull() || o.IsUnknown() {
		return v4, v6
	}
	attrs := o.Attributes()
	if b, ok := attrs["ipv4_enabled"].(types.Bool); ok && !b.IsNull() && !b.IsUnknown() {
		v4 = b.ValueBool()
	}
	if b, ok := attrs["ipv6_enabled"].(types.Bool); ok && !b.IsNull() && !b.IsUnknown() {
		v6 = b.ValueBool()
	}
	return v4, v6
}

func publicNetChanged(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.State.Raw.IsNull() {
		return
	}
	s4, s6 := publicNetFlags(req.StateValue)
	p4, p6 := publicNetFlags(req.PlanValue)
	resp.RequiresReplace = s4 != p4 || s6 != p6
}

func (r *serverResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func (r *serverResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m serverModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validatePrimaryNetwork(ctx, m)...)
	if m.Network.IsNull() || m.Network.IsUnknown() {
		return
	}
	var nets []serverNetworkBlock
	resp.Diagnostics.Append(m.Network.ElementsAs(ctx, &nets, false)...)
	seen := map[int64]bool{}
	for _, n := range nets {
		if n.NetworkID.IsUnknown() {
			continue
		}
		if seen[n.NetworkID.ValueInt64()] {
			resp.Diagnostics.AddAttributeError(path.Root("network"), "Duplicate network",
				fmt.Sprintf("Network %d is listed more than once.", n.NetworkID.ValueInt64()))
		}
		seen[n.NetworkID.ValueInt64()] = true
	}
}

const errPrimaryFixedIP = "The primary network of a server without public IPs is attached at creation, " +
	"where no fixed IP can be given; remove `ip` from its `network` block."

// validatePrimaryNetwork checks primary_network_id against public_net and
// the network blocks. Unknown values are not checked.
func validatePrimaryNetwork(ctx context.Context, m serverModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if m.PublicNet.IsUnknown() || m.PrimaryNetworkID.IsUnknown() || m.Network.IsUnknown() {
		return diags
	}
	if m.PublicNet.Attributes() != nil {
		for _, k := range []string{"ipv4_enabled", "ipv6_enabled"} {
			if b, ok := m.PublicNet.Attributes()[k].(types.Bool); ok && b.IsUnknown() {
				return diags
			}
		}
	}
	v4, v6 := publicNetFlags(m.PublicNet)
	private := !v4 && !v6
	var nets []serverNetworkBlock
	if !m.Network.IsNull() {
		diags.Append(m.Network.ElementsAs(ctx, &nets, false)...)
	}
	if !m.PrimaryNetworkID.IsNull() && !private {
		diags.AddAttributeError(path.Root("primary_network_id"), "Server has public IPs",
			"`primary_network_id` is only for servers without public IPs (`public_net` with `ipv4_enabled` and `ipv6_enabled` set to false).")
		return diags
	}
	if !private {
		return diags
	}
	if m.PrimaryNetworkID.IsNull() {
		if len(nets) != 1 {
			diags.AddAttributeError(path.Root("primary_network_id"), "Missing primary network",
				"A server without public IPs needs a network at creation: set `primary_network_id` or exactly one `network` block.")
		} else if !nets[0].IP.IsNull() && !nets[0].IP.IsUnknown() {
			diags.AddAttributeError(path.Root("network"), "Fixed IP in the primary network", errPrimaryFixedIP)
		}
		return diags
	}
	if m.Network.IsNull() {
		return diags
	}
	for _, n := range nets {
		if n.NetworkID.IsUnknown() || n.NetworkID.ValueInt64() != m.PrimaryNetworkID.ValueInt64() {
			continue
		}
		if !n.IP.IsNull() && !n.IP.IsUnknown() {
			diags.AddAttributeError(path.Root("network"), "Fixed IP in the primary network", errPrimaryFixedIP)
		}
		return diags
	}
	for _, n := range nets {
		if n.NetworkID.IsUnknown() {
			return diags
		}
	}
	diags.AddAttributeError(path.Root("primary_network_id"), "Primary network not listed",
		"`primary_network_id` must be one of the `network` blocks.")
	return diags
}

// primaryNetwork returns the network a server without public IPs is created
// in (nil for a server with public IPs).
func primaryNetwork(plan serverModel, nets []serverNetworkBlock) *int64 {
	if id := optInt64(plan.PrimaryNetworkID); id != nil {
		return id
	}
	if v4, v6 := publicNetFlags(plan.PublicNet); v4 || v6 || len(nets) != 1 || nets[0].NetworkID.IsUnknown() {
		return nil
	}
	return ptr(nets[0].NetworkID.ValueInt64())
}

// ModifyPlan keeps the automatically assigned private IPs of unchanged
// network blocks, so that unrelated updates do not show them as unknown.
func (r *serverResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state serverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Network.IsNull() || plan.Network.IsUnknown() || state.Network.IsNull() {
		return
	}
	var pn, sn []serverNetworkBlock
	resp.Diagnostics.Append(plan.Network.ElementsAs(ctx, &pn, false)...)
	resp.Diagnostics.Append(state.Network.ElementsAs(ctx, &sn, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	stateIP := map[int64]types.String{}
	for _, n := range sn {
		stateIP[n.NetworkID.ValueInt64()] = n.IP
	}
	changed := false
	for i, n := range pn {
		if n.IP.IsUnknown() && !n.NetworkID.IsUnknown() {
			if ip, ok := stateIP[n.NetworkID.ValueInt64()]; ok && !ip.IsNull() {
				pn[i].IP = ip
				changed = true
			}
		}
	}
	if !changed {
		return
	}
	set, d := types.SetValueFrom(ctx, types.ObjectType{AttrTypes: serverNetworkAttrTypes}, pn)
	resp.Diagnostics.Append(d...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("network"), set)...)
	}
}

func (r *serverResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 60*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	sshKeys, d := stringsFrom(ctx, plan.SSHKeys)
	resp.Diagnostics.Append(d...)
	var nets []serverNetworkBlock
	if !plan.Network.IsNull() && !plan.Network.IsUnknown() {
		resp.Diagnostics.Append(plan.Network.ElementsAs(ctx, &nets, false)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	body := client.CreateServerJSONRequestBody{
		Name:       plan.Name.ValueString(),
		ServerType: plan.ServerType.ValueString(),
		Image:      plan.Image.ValueString(),
		Location:   plan.Location.ValueString(),
		Labels:     &labels,
		UserData:   optString(plan.UserData),
	}
	if len(sshKeys) > 0 {
		body.SshKeys = &sshKeys
	}
	if !plan.PlacementGroupID.IsNull() && !plan.PlacementGroupID.IsUnknown() {
		body.PlacementGroup = ptr(strconv.FormatInt(plan.PlacementGroupID.ValueInt64(), 10))
	}
	if !plan.PublicNet.IsNull() && !plan.PublicNet.IsUnknown() {
		v4, v6 := publicNetFlags(plan.PublicNet)
		body.PublicNet = &struct {
			EnableIpv4 *bool `json:"enable_ipv4,omitempty"`
			EnableIpv6 *bool `json:"enable_ipv6,omitempty"`
		}{EnableIpv4: ptr(v4), EnableIpv6: ptr(v6)}
	}
	// Networks without a fixed IP are attached at creation; the others with
	// attach_to_network afterwards (the create call takes no IPs). The
	// primary network of a server without public IPs must come first.
	var createNets []string
	var laterNets []serverNetworkBlock
	primary := primaryNetwork(plan, nets)
	if primary != nil {
		createNets = append(createNets, client.FormatID(*primary))
	}
	for _, n := range nets {
		if primary != nil && n.NetworkID.ValueInt64() == *primary {
			continue
		}
		if n.IP.IsNull() || n.IP.IsUnknown() || n.IP.ValueString() == "" {
			createNets = append(createNets, client.FormatID(n.NetworkID.ValueInt64()))
		} else {
			laterNets = append(laterNets, n)
		}
	}
	if len(createNets) > 0 {
		body.Networks = &createNets
	}
	fwIDs, d := int64sFrom(ctx, plan.FirewallIDs)
	resp.Diagnostics.Append(d...)
	if len(fwIDs) > 0 {
		fws := make([]string, 0, len(fwIDs))
		for _, id := range fwIDs {
			fws = append(fws, client.FormatID(id))
		}
		body.Firewalls = &fws
	}

	res, err := client.Result(r.c.CreateServerWithResponse(ctx, body))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating server", err)
		return
	}
	srv := res.JSON201.Server
	plan.ID = types.StringValue(client.FormatID(srv.Id))
	// Save the ID right away: if anything below fails the server is tainted, not lost.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	if err := r.c.WaitForAction(ctx, &res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for server creation", err)
		return
	}

	for _, n := range laterNets {
		if err := r.attachNetwork(ctx, srv.Id, n); err != nil {
			apiErr(&resp.Diagnostics, "Error attaching server to network", err)
			return
		}
	}
	if plan.Backups.ValueBool() {
		if err := r.setBackups(ctx, srv.Id, true); err != nil {
			apiErr(&resp.Diagnostics, "Error enabling backups", err)
			return
		}
	}
	if plan.DeleteProtection.ValueBool() {
		if _, err := client.Result(r.c.UpdateServerWithResponse(ctx, srv.Id, client.UpdateServerJSONRequestBody{
			Protection: &struct {
				Delete *bool `json:"delete,omitempty"`
			}{Delete: ptr(true)},
		})); err != nil {
			apiErr(&resp.Diagnostics, "Error enabling delete protection", err)
			return
		}
	}

	resp.Diagnostics.Append(r.readInto(ctx, srv.Id, &plan, nil)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serverResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid server ID", err.Error())
		return
	}
	var gone bool
	resp.Diagnostics.Append(r.readInto(ctx, id, &state, &gone)...)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// readInto refreshes m from the API. If gone is non-nil, a 404 sets it
// instead of producing an error.
func (r *serverResource) readInto(ctx context.Context, id int64, m *serverModel, gone *bool) diag.Diagnostics {
	var diags diag.Diagnostics
	res, err := client.Result(r.c.GetServerWithResponse(ctx, id))
	if err != nil {
		if gone != nil && client.IsNotFound(err) {
			*gone = true
			return diags
		}
		diags.AddError("Error reading server", err.Error())
		return diags
	}
	diags.Append(serverToModel(ctx, &res.JSON200.Server, m)...)
	if !m.FirewallIDs.IsNull() {
		ids, err := serverFirewallIDs(ctx, r.c, id)
		if err != nil {
			diags.AddError("Error reading firewalls of the server", err.Error())
			return diags
		}
		m.FirewallIDs = int64SetValue(ids)
	}
	return diags
}

// serverFirewallIDs returns the firewalls applied directly (type server) to
// the server. The server object does not list its firewalls, so all
// firewalls of the project are searched.
func serverFirewallIDs(ctx context.Context, c *client.Odinex, serverID int64) ([]int64, error) {
	res, err := client.Result(c.ListFirewallsWithResponse(ctx, &client.ListFirewallsParams{}))
	if err != nil {
		return nil, err
	}
	return firewallsOfServer(res.JSON200.Firewalls, serverID), nil
}

func firewallsOfServer(fws []client.Firewall, serverID int64) []int64 {
	ids := []int64{}
	for _, fw := range fws {
		for _, t := range fw.AppliedTo {
			if t.Type == "server" && t.Server != nil && *t.Server == serverID {
				ids = append(ids, fw.Id)
				break
			}
		}
	}
	return ids
}

// serverToModel copies the API server into the model, keeping values the
// API cannot return (ssh_keys, user_data, keep_disk) and the user's chosen
// spelling (name or ID) of image, server_type and location.
func serverToModel(ctx context.Context, s *client.Server, m *serverModel) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(client.FormatID(s.Id))
	m.Name = types.StringValue(s.Name)
	m.ServerType = types.StringValue(pickRef(m.ServerType, s.ServerType.Name, s.ServerType.Id))
	m.Location = types.StringValue(pickRef(m.Location, s.Location.Name, s.Location.Id))
	if s.Image != nil {
		name := ""
		if s.Image.Name != nil {
			name = *s.Image.Name
		}
		m.Image = types.StringValue(pickRef(m.Image, name, s.Image.Id))
	}
	m.Labels = labelsToMap(s.Labels)
	m.Backups = types.BoolValue(s.BackupWindow != nil)
	m.BackupWindow = types.StringValue(derefStr(s.BackupWindow))
	m.PlacementGroupID = int64PtrValue(s.PlacementGroup)
	m.PrimaryNetworkID = int64PtrValue(s.PrimaryNetwork)
	m.DeleteProtection = types.BoolValue(s.Protection.Delete)
	m.Status = types.StringValue(string(s.Status))
	m.PrimaryDiskSize = types.Int64Value(int64(s.PrimaryDiskSize))
	m.IPv4Address = types.StringValue("")
	m.IPv6Address = types.StringValue("")
	m.PrimaryIPv4ID = types.Int64Null()
	m.PrimaryIPv6ID = types.Int64Null()
	if s.PublicNet.Ipv4 != nil {
		m.IPv4Address = types.StringValue(derefStr(s.PublicNet.Ipv4.Ip))
		m.PrimaryIPv4ID = types.Int64Value(s.PublicNet.Ipv4.Id)
	}
	if s.PublicNet.Ipv6 != nil {
		m.IPv6Address = types.StringValue(derefStr(s.PublicNet.Ipv6.Ip))
		m.PrimaryIPv6ID = types.Int64Value(s.PublicNet.Ipv6.Id)
	}
	if m.KeepDisk.IsNull() || m.KeepDisk.IsUnknown() {
		m.KeepDisk = types.BoolValue(false)
	}
	if m.ShutdownBeforeDeletion.IsNull() || m.ShutdownBeforeDeletion.IsUnknown() {
		m.ShutdownBeforeDeletion = types.BoolValue(false)
	}
	if m.SSHKeys.IsUnknown() {
		m.SSHKeys = types.ListNull(types.StringType)
	}
	if m.UserData.IsUnknown() {
		m.UserData = types.StringNull()
	}
	// public_net is only tracked when it was configured.
	if !m.PublicNet.IsNull() {
		m.PublicNet = types.ObjectValueMust(serverPublicNetAttrTypes, map[string]attr.Value{
			"ipv4_enabled": types.BoolValue(s.PublicNet.Ipv4 != nil),
			"ipv6_enabled": types.BoolValue(s.PublicNet.Ipv6 != nil),
		})
	}
	// network blocks are only tracked when configured (so that
	// odinex_server_network can manage attachments instead).
	if !m.Network.IsNull() {
		elems := make([]attr.Value, 0, len(s.PrivateNet))
		for _, pn := range s.PrivateNet {
			elems = append(elems, types.ObjectValueMust(serverNetworkAttrTypes, map[string]attr.Value{
				"network_id": types.Int64Value(pn.Network),
				"ip":         types.StringValue(derefStr(pn.Ip)),
			}))
		}
		set, d := types.SetValue(types.ObjectType{AttrTypes: serverNetworkAttrTypes}, elems)
		diags.Append(d...)
		m.Network = set
	}
	_ = ctx
	return diags
}

// pickRef keeps the user's spelling of a name-or-ID reference when it still
// refers to the same object; otherwise it returns the name (or the ID when
// the object has no name).
func pickRef(cur types.String, name string, id int64) string {
	if !cur.IsNull() && !cur.IsUnknown() {
		v := cur.ValueString()
		if v == name || v == client.FormatID(id) {
			return v
		}
	}
	if name != "" {
		return name
	}
	return client.FormatID(id)
}

func (r *serverResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 60*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid server ID", err.Error())
		return
	}

	// Name, labels, protection.
	upd := client.UpdateServerJSONRequestBody{}
	changed := false
	if !plan.Name.Equal(state.Name) {
		upd.Name = ptr(plan.Name.ValueString())
		changed = true
	}
	if !plan.Labels.Equal(state.Labels) {
		labels, d := labelsFromMap(ctx, plan.Labels)
		resp.Diagnostics.Append(d...)
		upd.Labels = &labels
		changed = true
	}
	if !plan.DeleteProtection.Equal(state.DeleteProtection) {
		upd.Protection = &struct {
			Delete *bool `json:"delete,omitempty"`
		}{Delete: ptr(plan.DeleteProtection.ValueBool())}
		changed = true
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if changed {
		if _, err := client.Result(r.c.UpdateServerWithResponse(ctx, id, upd)); err != nil {
			apiErr(&resp.Diagnostics, "Error updating server", err)
			return
		}
	}

	// Server type: power off, change type, power on.
	if !plan.ServerType.Equal(state.ServerType) {
		if err := r.changeType(ctx, id, plan.ServerType.ValueString(), !plan.KeepDisk.ValueBool()); err != nil {
			apiErr(&resp.Diagnostics, "Error changing server type", err)
			return
		}
	}

	// Image: rebuild.
	if !plan.Image.Equal(state.Image) {
		res, err := client.Result(r.c.ServerRebuildWithResponse(ctx, id, client.ServerRebuildJSONRequestBody{Image: plan.Image.ValueString()}))
		if err == nil {
			err = r.c.WaitForAction(ctx, &res.JSON201.Action)
		}
		if err != nil {
			apiErr(&resp.Diagnostics, "Error rebuilding server", err)
			return
		}
	}

	if !plan.Backups.Equal(state.Backups) {
		if err := r.setBackups(ctx, id, plan.Backups.ValueBool()); err != nil {
			apiErr(&resp.Diagnostics, "Error changing backups", err)
			return
		}
	}

	if !plan.FirewallIDs.IsUnknown() && !plan.FirewallIDs.Equal(state.FirewallIDs) {
		have, d := int64sFrom(ctx, state.FirewallIDs)
		resp.Diagnostics.Append(d...)
		want, d := int64sFrom(ctx, plan.FirewallIDs)
		resp.Diagnostics.Append(d...)
		if state.FirewallIDs.IsNull() {
			// Not tracked before: start from what is applied now.
			cur, err := serverFirewallIDs(ctx, r.c, id)
			if err != nil {
				apiErr(&resp.Diagnostics, "Error reading firewalls of the server", err)
				return
			}
			have = cur
			if plan.FirewallIDs.IsNull() {
				want = cur
			}
		}
		add, remove := diffInt64(have, want)
		target := []client.FirewallTarget{{Type: "server", Server: ptr(id)}}
		for _, fw := range remove {
			if err := applyFirewallTargets(ctx, r.c, fw, nil, target); err != nil && !client.IsNotFound(err) {
				apiErr(&resp.Diagnostics, fmt.Sprintf("Error removing firewall %d from the server", fw), err)
				return
			}
		}
		for _, fw := range add {
			if err := applyFirewallTargets(ctx, r.c, fw, target, nil); err != nil {
				apiErr(&resp.Diagnostics, fmt.Sprintf("Error applying firewall %d to the server", fw), err)
				return
			}
		}
	}

	if !plan.Network.Equal(state.Network) && !plan.Network.IsUnknown() {
		resp.Diagnostics.Append(r.updateNetworks(ctx, id, state.Network, plan.Network)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	resp.Diagnostics.Append(r.readInto(ctx, id, &plan, nil)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serverResource) updateNetworks(ctx context.Context, id int64, stateSet, planSet types.Set) diag.Diagnostics {
	var diags diag.Diagnostics
	var have, want []serverNetworkBlock
	if !stateSet.IsNull() {
		diags.Append(stateSet.ElementsAs(ctx, &have, false)...)
	}
	if !planSet.IsNull() {
		diags.Append(planSet.ElementsAs(ctx, &want, false)...)
	}
	if diags.HasError() {
		return diags
	}
	haveBy := map[int64]serverNetworkBlock{}
	for _, n := range have {
		haveBy[n.NetworkID.ValueInt64()] = n
	}
	wantBy := map[int64]serverNetworkBlock{}
	for _, n := range want {
		wantBy[n.NetworkID.ValueInt64()] = n
	}
	// Detach removed networks and networks whose fixed IP changed.
	for nid, h := range haveBy {
		w, keep := wantBy[nid]
		ipChanged := keep && !w.IP.IsUnknown() && !w.IP.IsNull() && w.IP.ValueString() != h.IP.ValueString()
		if !keep || ipChanged {
			if err := r.detachNetwork(ctx, id, nid); err != nil {
				diags.AddError("Error detaching server from network", err.Error())
				return diags
			}
			if keep {
				delete(haveBy, nid)
			}
		}
	}
	for nid, w := range wantBy {
		if _, ok := haveBy[nid]; ok {
			continue
		}
		if err := r.attachNetwork(ctx, id, w); err != nil {
			diags.AddError("Error attaching server to network", err.Error())
			return diags
		}
	}
	return diags
}

func (r *serverResource) attachNetwork(ctx context.Context, id int64, n serverNetworkBlock) error {
	return attachServerToNetwork(ctx, r.c, id, n.NetworkID.ValueInt64(), optString(n.IP))
}

func (r *serverResource) detachNetwork(ctx context.Context, id, networkID int64) error {
	return detachServerFromNetwork(ctx, r.c, id, networkID)
}

func attachServerToNetwork(ctx context.Context, c *client.Odinex, serverID, networkID int64, ip *string) error {
	res, err := client.Result(c.ServerAttachToNetworkWithResponse(ctx, serverID, client.ServerAttachToNetworkJSONRequestBody{
		Network: client.FormatID(networkID),
		Ip:      ip,
	}))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func detachServerFromNetwork(ctx context.Context, c *client.Odinex, serverID, networkID int64) error {
	res, err := client.Result(c.ServerDetachFromNetworkWithResponse(ctx, serverID, client.ServerDetachFromNetworkJSONRequestBody{
		Network: client.FormatID(networkID),
	}))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func (r *serverResource) setBackups(ctx context.Context, id int64, enable bool) error {
	var a *client.Action
	if enable {
		res, err := client.Result(r.c.ServerEnableBackupWithResponse(ctx, id))
		if err != nil {
			return err
		}
		a = &res.JSON201.Action
	} else {
		res, err := client.Result(r.c.ServerDisableBackupWithResponse(ctx, id))
		if err != nil {
			return err
		}
		a = &res.JSON201.Action
	}
	return r.c.WaitForAction(ctx, a)
}

// changeType powers the server off (if needed), changes its type and powers
// it on again if it was running before.
func (r *serverResource) changeType(ctx context.Context, id int64, serverType string, upgradeDisk bool) error {
	res, err := client.Result(r.c.GetServerWithResponse(ctx, id))
	if err != nil {
		return err
	}
	wasRunning := res.JSON200.Server.Status != "off"
	if wasRunning {
		po, err := client.Result(r.c.ServerPoweroffWithResponse(ctx, id))
		if err != nil {
			return fmt.Errorf("powering off: %w", err)
		}
		if err := r.c.WaitForAction(ctx, &po.JSON201.Action); err != nil {
			return fmt.Errorf("powering off: %w", err)
		}
	}
	ct, err := client.Result(r.c.ServerChangeTypeWithResponse(ctx, id, client.ServerChangeTypeJSONRequestBody{
		ServerType:  serverType,
		UpgradeDisk: upgradeDisk,
	}))
	if err != nil {
		return err
	}
	if err := r.c.WaitForAction(ctx, &ct.JSON201.Action); err != nil {
		return err
	}
	if wasRunning {
		on, err := client.Result(r.c.ServerPoweronWithResponse(ctx, id))
		if err != nil {
			return fmt.Errorf("powering on: %w", err)
		}
		if err := r.c.WaitForAction(ctx, &on.JSON201.Action); err != nil {
			return fmt.Errorf("powering on: %w", err)
		}
	}
	return nil
}

func (r *serverResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid server ID", err.Error())
		return
	}
	if state.ShutdownBeforeDeletion.ValueBool() {
		if res, err := client.Result(r.c.ServerShutdownWithResponse(ctx, id)); err == nil {
			_ = r.c.WaitForAction(ctx, &res.JSON201.Action)
			r.waitForStatus(ctx, id, "off", 5*time.Minute)
		}
	}
	res, err := client.Result(r.c.DeleteServerWithResponse(ctx, id, nil))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		apiErr(&resp.Diagnostics, "Error deleting server", err)
		return
	}
	if err := r.c.WaitForAction(ctx, &res.JSON200.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for server deletion", err)
	}
}

func (r *serverResource) waitForStatus(ctx context.Context, id int64, status string, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		res, err := client.Result(r.c.GetServerWithResponse(ctx, id))
		if err != nil || string(res.JSON200.Server.Status) == status {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.c.PollInterval):
		}
	}
}

func (r *serverResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := client.ParseID(req.ID)
	if err != nil {
		// Import by name.
		res, lerr := client.Result(r.c.ListServersWithResponse(ctx, &client.ListServersParams{Name: &req.ID}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error looking up server by name", lerr)
			return
		}
		if len(res.JSON200.Servers) != 1 {
			resp.Diagnostics.AddError("Server not found", fmt.Sprintf("No server with ID or name %q.", req.ID))
			return
		}
		id = res.JSON200.Servers[0].Id
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	// network and public_net stay null after import; ssh_keys/user_data
	// cannot be read back from the API.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("network"), types.SetNull(types.ObjectType{AttrTypes: serverNetworkAttrTypes}))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("public_net"), types.ObjectNull(serverPublicNetAttrTypes))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_ids"), types.SetNull(types.Int64Type))...)
}
