package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

func requiredIDAttr(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{
		Required:            true,
		MarkdownDescription: desc,
		PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
	}
}

// ============================================================ odinex_volume_attachment

// NewVolumeAttachmentResource returns the odinex_volume_attachment resource.
func NewVolumeAttachmentResource() resource.Resource { return &volumeAttachmentResource{} }

type volumeAttachmentResource struct{ c *client.Odinex }

type volumeAttachmentModel struct {
	ID       types.String `tfsdk:"id"`
	VolumeID types.Int64  `tfsdk:"volume_id"`
	ServerID types.Int64  `tfsdk:"server_id"`
}

func (r *volumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_attachment"
}

func (r *volumeAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a volume to a server. The ID is the volume ID. Do not set `server_id` on the `odinex_volume` at the same time.",
		Attributes: map[string]schema.Attribute{
			"id":        idAttribute(),
			"volume_id": requiredIDAttr("ID of the volume."),
			"server_id": requiredIDAttr("ID of the server."),
		},
	}
}

func (r *volumeAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func (r *volumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vid, sid := plan.VolumeID.ValueInt64(), plan.ServerID.ValueInt64()
	g, err := client.Result(r.c.GetVolumeWithResponse(ctx, vid))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading volume", err)
		return
	}
	if cur := g.JSON200.Volume.Server; cur == nil || *cur != sid {
		if cur != nil {
			if err := detachVolume(ctx, r.c, vid); err != nil {
				apiErr(&resp.Diagnostics, "Error detaching volume from its current server", err)
				return
			}
		}
		if err := attachVolume(ctx, r.c, vid, sid); err != nil {
			apiErr(&resp.Diagnostics, "Error attaching volume", err)
			return
		}
	}
	plan.ID = types.StringValue(client.FormatID(vid))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vid, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid volume attachment ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetVolumeWithResponse(ctx, vid))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading volume", err)
		return
	}
	if g.JSON200.Volume.Server == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.VolumeID = types.Int64Value(vid)
	state.ServerID = types.Int64Value(*g.JSON200.Volume.Server)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *volumeAttachmentResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// All attributes force replacement.
}

func (r *volumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vid := state.VolumeID.ValueInt64()
	g, err := client.Result(r.c.GetVolumeWithResponse(ctx, vid))
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading volume", err)
		return
	}
	if g.JSON200.Volume.Server == nil || *g.JSON200.Volume.Server != state.ServerID.ValueInt64() {
		return
	}
	if err := detachVolume(ctx, r.c, vid); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error detaching volume", err)
	}
}

func (r *volumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", "Import a volume attachment by the volume ID.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ============================================================ odinex_floating_ip_assignment

// NewFloatingIPAssignmentResource returns the odinex_floating_ip_assignment resource.
func NewFloatingIPAssignmentResource() resource.Resource { return &floatingIPAssignmentResource{} }

type floatingIPAssignmentResource struct{ c *client.Odinex }

type floatingIPAssignmentModel struct {
	ID           types.String `tfsdk:"id"`
	FloatingIPID types.Int64  `tfsdk:"floating_ip_id"`
	ServerID     types.Int64  `tfsdk:"server_id"`
}

func (r *floatingIPAssignmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_floating_ip_assignment"
}

func (r *floatingIPAssignmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Assigns a floating IP to a server. The ID is the floating IP ID. Do not set `server_id` on the `odinex_floating_ip` at the same time.",
		Attributes: map[string]schema.Attribute{
			"id":             idAttribute(),
			"floating_ip_id": requiredIDAttr("ID of the floating IP."),
			"server_id":      requiredIDAttr("ID of the server."),
		},
	}
}

func (r *floatingIPAssignmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func assignFloatingIP(ctx context.Context, c *client.Odinex, fipID, serverID int64) error {
	res, err := client.Result(c.AssignFloatingIpWithResponse(ctx, fipID, client.AssignFloatingIpJSONRequestBody{Server: serverID}))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func unassignFloatingIP(ctx context.Context, c *client.Odinex, fipID int64) error {
	res, err := client.Result(c.UnassignFloatingIpWithResponse(ctx, fipID))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func (r *floatingIPAssignmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan floatingIPAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := assignFloatingIP(ctx, r.c, plan.FloatingIPID.ValueInt64(), plan.ServerID.ValueInt64()); err != nil {
		apiErr(&resp.Diagnostics, "Error assigning floating IP", err)
		return
	}
	plan.ID = types.StringValue(client.FormatID(plan.FloatingIPID.ValueInt64()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *floatingIPAssignmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state floatingIPAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fid, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid floating IP assignment ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetFloatingIpWithResponse(ctx, fid))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading floating IP", err)
		return
	}
	if g.JSON200.FloatingIp.Server == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.FloatingIPID = types.Int64Value(fid)
	state.ServerID = types.Int64Value(*g.JSON200.FloatingIp.Server)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *floatingIPAssignmentResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *floatingIPAssignmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state floatingIPAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fid := state.FloatingIPID.ValueInt64()
	g, err := client.Result(r.c.GetFloatingIpWithResponse(ctx, fid))
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading floating IP", err)
		return
	}
	if g.JSON200.FloatingIp.Server == nil {
		return
	}
	if err := unassignFloatingIP(ctx, r.c, fid); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error unassigning floating IP", err)
	}
}

func (r *floatingIPAssignmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", "Import a floating IP assignment by the floating IP ID.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ============================================================ odinex_elastic_ip_assignment

// NewElasticIPAssignmentResource returns the odinex_elastic_ip_assignment resource.
func NewElasticIPAssignmentResource() resource.Resource { return &elasticIPAssignmentResource{} }

type elasticIPAssignmentResource struct{ c *client.Odinex }

type elasticIPAssignmentModel struct {
	ID          types.String `tfsdk:"id"`
	ElasticIPID types.Int64  `tfsdk:"elastic_ip_id"`
	ServerID    types.Int64  `tfsdk:"server_id"`
}

func (r *elasticIPAssignmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_elastic_ip_assignment"
}

func (r *elasticIPAssignmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Assigns an Elastic IP to a server without public IPs. The ID is the Elastic IP ID. Do not set `server_id` on the `odinex_elastic_ip` at the same time.",
		Attributes: map[string]schema.Attribute{
			"id":            idAttribute(),
			"elastic_ip_id": requiredIDAttr("ID of the Elastic IP."),
			"server_id":     requiredIDAttr("ID of the server."),
		},
	}
}

func (r *elasticIPAssignmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func assignElasticIP(ctx context.Context, c *client.Odinex, eipID, serverID int64) error {
	res, err := client.Result(c.AssignElasticIpWithResponse(ctx, eipID, client.AssignElasticIpJSONRequestBody{Server: serverID}))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func unassignElasticIP(ctx context.Context, c *client.Odinex, eipID int64) error {
	res, err := client.Result(c.UnassignElasticIpWithResponse(ctx, eipID))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func (r *elasticIPAssignmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan elasticIPAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := assignElasticIP(ctx, r.c, plan.ElasticIPID.ValueInt64(), plan.ServerID.ValueInt64()); err != nil {
		apiErr(&resp.Diagnostics, "Error assigning Elastic IP", err)
		return
	}
	plan.ID = types.StringValue(client.FormatID(plan.ElasticIPID.ValueInt64()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *elasticIPAssignmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state elasticIPAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	eid, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid Elastic IP assignment ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetElasticIpWithResponse(ctx, eid))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading Elastic IP", err)
		return
	}
	if g.JSON200.ElasticIp.Server == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ElasticIPID = types.Int64Value(eid)
	state.ServerID = types.Int64Value(*g.JSON200.ElasticIp.Server)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *elasticIPAssignmentResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *elasticIPAssignmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state elasticIPAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	eid := state.ElasticIPID.ValueInt64()
	g, err := client.Result(r.c.GetElasticIpWithResponse(ctx, eid))
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading Elastic IP", err)
		return
	}
	if g.JSON200.ElasticIp.Server == nil {
		return
	}
	if err := unassignElasticIP(ctx, r.c, eid); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error unassigning Elastic IP", err)
	}
}

func (r *elasticIPAssignmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", "Import an Elastic IP assignment by the Elastic IP ID.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ============================================================ odinex_server_network

// NewServerNetworkResource returns the odinex_server_network resource.
func NewServerNetworkResource() resource.Resource { return &serverNetworkResource{} }

type serverNetworkResource struct{ c *client.Odinex }

type serverNetworkModel struct {
	ID        types.String `tfsdk:"id"`
	ServerID  types.Int64  `tfsdk:"server_id"`
	NetworkID types.Int64  `tfsdk:"network_id"`
	IP        types.String `tfsdk:"ip"`
}

func (r *serverNetworkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_network"
}

func (r *serverNetworkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a server to a private network. The ID is `<server_id>-<network_id>`. " +
			"Do not use `network` blocks on the same `odinex_server` at the same time.",
		Attributes: map[string]schema.Attribute{
			"id":         idAttribute(),
			"server_id":  requiredIDAttr("ID of the server."),
			"network_id": requiredIDAttr("ID of the network."),
			"ip": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "IP of the server inside the network; assigned automatically when omitted.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *serverNetworkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func parseServerNetworkID(id string) (int64, int64, error) {
	parts := strings.Split(id, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid ID %q: expected <server_id>-<network_id>", id)
	}
	sid, err := client.ParseID(parts[0])
	if err != nil {
		return 0, 0, err
	}
	nid, err := client.ParseID(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return sid, nid, nil
}

func (r *serverNetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverNetworkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sid, nid := plan.ServerID.ValueInt64(), plan.NetworkID.ValueInt64()
	if err := attachServerToNetwork(ctx, r.c, sid, nid, optString(plan.IP)); err != nil {
		apiErr(&resp.Diagnostics, "Error attaching server to network", err)
		return
	}
	plan.ID = types.StringValue(fmt.Sprintf("%d-%d", sid, nid))
	found, ip, err := r.lookup(ctx, sid, nid)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading server", err)
		return
	}
	if !found {
		resp.Diagnostics.AddError("Attachment not found", fmt.Sprintf("Server %d is not attached to network %d after the action succeeded.", sid, nid))
		return
	}
	plan.IP = types.StringValue(ip)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// lookup returns whether the server is attached to the network and its IP.
func (r *serverNetworkResource) lookup(ctx context.Context, sid, nid int64) (bool, string, error) {
	g, err := client.Result(r.c.GetServerWithResponse(ctx, sid))
	if err != nil {
		return false, "", err
	}
	for _, pn := range g.JSON200.Server.PrivateNet {
		if pn.Network == nid {
			return true, derefStr(pn.Ip), nil
		}
	}
	return false, "", nil
}

func (r *serverNetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serverNetworkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sid, nid, err := parseServerNetworkID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid server network ID", err.Error())
		return
	}
	found, ip, err := r.lookup(ctx, sid, nid)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading server", err)
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ServerID = types.Int64Value(sid)
	state.NetworkID = types.Int64Value(nid)
	state.IP = types.StringValue(ip)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverNetworkResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *serverNetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serverNetworkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := detachServerFromNetwork(ctx, r.c, state.ServerID.ValueInt64(), state.NetworkID.ValueInt64())
	if err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error detaching server from network", err)
	}
}

func (r *serverNetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, _, err := parseServerNetworkID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
