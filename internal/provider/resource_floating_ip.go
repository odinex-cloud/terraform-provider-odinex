package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// ============================================================ odinex_floating_ip

// NewFloatingIPResource returns the odinex_floating_ip resource.
func NewFloatingIPResource() resource.Resource { return &floatingIPResource{} }

type floatingIPResource struct{ c *client.Odinex }

type floatingIPModel struct {
	ID               types.String   `tfsdk:"id"`
	Type             types.String   `tfsdk:"type"`
	Name             types.String   `tfsdk:"name"`
	Description      types.String   `tfsdk:"description"`
	HomeLocation     types.String   `tfsdk:"home_location"`
	ServerID         types.Int64    `tfsdk:"server_id"`
	IPAddress        types.String   `tfsdk:"ip_address"`
	Labels           types.Map      `tfsdk:"labels"`
	DeleteProtection types.Bool     `tfsdk:"delete_protection"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (r *floatingIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_floating_ip"
}

func (r *floatingIPResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a floating IP. Assign it with `server_id` or with `odinex_floating_ip_assignment` (not both).",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute(),
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`ipv4` or `ipv6`.",
				Validators:          []validator.String{stringvalidator.OneOf("ipv4", "ipv6")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the floating IP.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Description of the floating IP.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"home_location": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Location of the floating IP. Required unless `server_id` is set.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"server_id": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Server the IP is assigned to.",
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

func (r *floatingIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func floatingIPToModel(f *client.FloatingIp, m *floatingIPModel) {
	m.ID = types.StringValue(client.FormatID(f.Id))
	m.Type = types.StringValue(string(f.Type))
	m.Name = types.StringValue(f.Name)
	m.Description = types.StringValue(f.Description)
	m.HomeLocation = types.StringValue(f.Location)
	m.ServerID = int64PtrValue(f.Server)
	m.IPAddress = types.StringValue(derefStr(f.Ip))
	m.Labels = labelsToMap(f.Labels)
	m.DeleteProtection = types.BoolValue(f.Protection.Delete)
}

func (r *floatingIPResource) get(ctx context.Context, id int64) (*client.FloatingIp, error) {
	g, err := client.Result(r.c.GetFloatingIpWithResponse(ctx, id))
	if err != nil {
		return nil, err
	}
	return &g.JSON200.FloatingIp, nil
}

func (r *floatingIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan floatingIPModel
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
	if optString(plan.HomeLocation) == nil && optInt64(plan.ServerID) == nil {
		resp.Diagnostics.AddAttributeError(path.Root("home_location"), "Missing location", "Set `home_location` or `server_id`.")
		return
	}
	res, err := client.Result(r.c.CreateFloatingIpWithResponse(ctx, client.CreateFloatingIpJSONRequestBody{
		Type:        client.CreateFloatingIpJSONBodyType(plan.Type.ValueString()),
		Name:        optString(plan.Name),
		Description: optString(plan.Description),
		Location:    optString(plan.HomeLocation),
		Server:      optInt64(plan.ServerID),
		Labels:      &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating floating IP", err)
		return
	}
	id := res.JSON201.FloatingIp.Id
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	if err := r.c.WaitForAction(ctx, res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for floating IP creation", err)
		return
	}
	if plan.DeleteProtection.ValueBool() {
		if _, err := client.Result(r.c.UpdateFloatingIpWithResponse(ctx, id, client.UpdateFloatingIpJSONRequestBody{
			Protection: &struct {
				Delete *bool `json:"delete,omitempty"`
			}{Delete: ptr(true)},
		})); err != nil {
			apiErr(&resp.Diagnostics, "Error enabling delete protection", err)
			return
		}
	}
	f, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading floating IP", err)
		return
	}
	floatingIPToModel(f, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *floatingIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state floatingIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid floating IP ID", err.Error())
		return
	}
	f, err := r.get(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading floating IP", err)
		return
	}
	floatingIPToModel(f, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *floatingIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state floatingIPModel
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
	body := client.UpdateFloatingIpJSONRequestBody{
		Labels: &labels,
		Protection: &struct {
			Delete *bool `json:"delete,omitempty"`
		}{Delete: ptr(plan.DeleteProtection.ValueBool())},
	}
	if !plan.Name.Equal(state.Name) && !plan.Name.IsUnknown() {
		body.Name = ptr(plan.Name.ValueString())
	}
	if !plan.Description.Equal(state.Description) && !plan.Description.IsUnknown() {
		body.Description = ptr(plan.Description.ValueString())
	}
	if _, err := client.Result(r.c.UpdateFloatingIpWithResponse(ctx, id, body)); err != nil {
		apiErr(&resp.Diagnostics, "Error updating floating IP", err)
		return
	}
	if !plan.ServerID.IsUnknown() && !plan.ServerID.Equal(state.ServerID) {
		var err error
		if plan.ServerID.IsNull() {
			err = unassignFloatingIP(ctx, r.c, id)
		} else {
			err = assignFloatingIP(ctx, r.c, id, plan.ServerID.ValueInt64())
		}
		if err != nil {
			apiErr(&resp.Diagnostics, "Error changing floating IP assignment", err)
			return
		}
	}
	f, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading floating IP", err)
		return
	}
	floatingIPToModel(f, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *floatingIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state floatingIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())
	res, err := client.Result(r.c.DeleteFloatingIpWithResponse(ctx, id))
	if client.IsNotFound(err) {
		return
	}
	if err == nil {
		err = r.c.WaitForAction(ctx, &res.JSON200.Action)
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error deleting floating IP", err)
	}
}

func (r *floatingIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListFloatingIpsWithResponse(ctx, &client.ListFloatingIpsParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing floating IPs", lerr)
			return
		}
		for _, f := range res.JSON200.FloatingIps {
			if f.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(f.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("Floating IP not found", fmt.Sprintf("No floating IP with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ============================================================ odinex_snapshot

// NewSnapshotResource returns the odinex_snapshot resource.
func NewSnapshotResource() resource.Resource { return &snapshotResource{} }

type snapshotResource struct{ c *client.Odinex }

type snapshotModel struct {
	ID          types.String   `tfsdk:"id"`
	ServerID    types.Int64    `tfsdk:"server_id"`
	Description types.String   `tfsdk:"description"`
	Labels      types.Map      `tfsdk:"labels"`
	ImageSize   types.Float64  `tfsdk:"image_size"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

func (r *snapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot"
}

func (r *snapshotResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a snapshot image of a server's disk. Use the snapshot ID as `image` of an `odinex_server`.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute(),
			"server_id": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "ID of the server to snapshot. Changing it creates a new snapshot.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Description of the snapshot.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"labels": labelsAttribute(),
			"image_size": schema.Float64Attribute{
				Computed:            true,
				MarkdownDescription: "Size of the snapshot in GB.",
				PlanModifiers:       []planmodifier.Float64{float64planmodifier.UseStateForUnknown()},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *snapshotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func snapshotToModel(img *client.Image, m *snapshotModel) {
	m.ID = types.StringValue(client.FormatID(img.Id))
	if img.CreatedFrom != nil {
		m.ServerID = types.Int64Value(*img.CreatedFrom)
	}
	m.Description = types.StringValue(img.Description)
	m.Labels = labelsToMap(img.Labels)
	if img.ImageSize != nil {
		m.ImageSize = types.Float64Value(float64(*img.ImageSize))
	} else {
		m.ImageSize = types.Float64Null()
	}
}

func (r *snapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan snapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 60*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := client.Result(r.c.ServerCreateImageWithResponse(ctx, plan.ServerID.ValueInt64(), client.ServerCreateImageJSONRequestBody{
		Description: optString(plan.Description),
		Labels:      &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating snapshot", err)
		return
	}
	var imageID int64
	if res.JSON201.Image != nil {
		imageID = res.JSON201.Image.Id
	} else {
		for _, rs := range res.JSON201.Action.Resources {
			if rs.Type == "image" {
				imageID = rs.Id
			}
		}
	}
	if imageID == 0 {
		resp.Diagnostics.AddError("Error creating snapshot", "The API response contains no image ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(imageID))...)
	if err := r.c.WaitForAction(ctx, &res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for snapshot creation", err)
		return
	}
	g, err := client.Result(r.c.GetImageWithResponse(ctx, imageID))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading snapshot", err)
		return
	}
	serverID := plan.ServerID
	snapshotToModel(&g.JSON200.Image, &plan)
	plan.ServerID = serverID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *snapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state snapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid snapshot ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetImageWithResponse(ctx, id))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading snapshot", err)
		return
	}
	// Keep server_id when the API no longer knows the source server.
	serverID := state.ServerID
	snapshotToModel(&g.JSON200.Image, &state)
	if g.JSON200.Image.CreatedFrom == nil {
		state.ServerID = serverID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *snapshotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state snapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	body := client.UpdateImageJSONRequestBody{Labels: &labels}
	if !plan.Description.IsUnknown() {
		body.Description = ptr(plan.Description.ValueString())
	}
	res, err := client.Result(r.c.UpdateImageWithResponse(ctx, id, body))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error updating snapshot", err)
		return
	}
	serverID := plan.ServerID
	snapshotToModel(&res.JSON200.Image, &plan)
	plan.ServerID = serverID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *snapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state snapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	if _, err := client.Result(r.c.DeleteImageWithResponse(ctx, id)); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error deleting snapshot", err)
	}
}

func (r *snapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
