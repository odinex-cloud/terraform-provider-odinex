package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

var _ resource.ResourceWithImportState = (*volumeResource)(nil)

// NewVolumeResource returns the odinex_volume resource.
func NewVolumeResource() resource.Resource { return &volumeResource{} }

type volumeResource struct{ c *client.Odinex }

type volumeModel struct {
	ID               types.String   `tfsdk:"id"`
	Name             types.String   `tfsdk:"name"`
	Size             types.Int64    `tfsdk:"size"`
	Location         types.String   `tfsdk:"location"`
	ServerID         types.Int64    `tfsdk:"server_id"`
	Labels           types.Map      `tfsdk:"labels"`
	DeleteProtection types.Bool     `tfsdk:"delete_protection"`
	LinuxDevice      types.String   `tfsdk:"linux_device"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a block storage volume. The size can grow in place; shrinking recreates the volume.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name of the volume."},
			"size": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Size in GB (10–10240). Growing is done online; shrinking recreates the volume.",
				Validators:          []validator.Int64{int64validator.Between(10, 10240)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull() && !req.PlanValue.IsUnknown() && req.PlanValue.ValueInt64() < req.StateValue.ValueInt64()
				}, "Shrinking a volume recreates it.", "Shrinking a volume recreates it.")},
			},
			"location": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Location of the volume. Required unless `server_id` is set (then the server's location is used).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"server_id": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Server to attach the volume to. Changing it detaches/attaches in place. Use either this or `odinex_volume_attachment`.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"labels":            labelsAttribute(),
			"delete_protection": deleteProtectionAttribute(),
			"linux_device": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Device path of the volume inside the server.",
				PlanModifiers:       []planmodifier.String{useStateUnless("server_id")},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func volumeToModel(v *client.Volume, m *volumeModel, trackServer bool) {
	m.ID = types.StringValue(client.FormatID(v.Id))
	m.Name = types.StringValue(v.Name)
	m.Size = types.Int64Value(int64(v.Size))
	m.Location = types.StringValue(v.Location)
	if trackServer {
		m.ServerID = int64PtrValue(v.Server)
	}
	m.Labels = labelsToMap(v.Labels)
	m.DeleteProtection = types.BoolValue(v.Protection.Delete)
	m.LinuxDevice = types.StringValue(derefStr(v.LinuxDevice))
}

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeModel
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
	if plan.Location.IsUnknown() && plan.ServerID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("location"), "Missing location", "Set `location` or `server_id`.")
		return
	}
	res, err := client.Result(r.c.CreateVolumeWithResponse(ctx, client.CreateVolumeJSONRequestBody{
		Name:     plan.Name.ValueString(),
		Size:     int(plan.Size.ValueInt64()),
		Location: optString(plan.Location),
		Server:   optInt64(plan.ServerID),
		Labels:   &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating volume", err)
		return
	}
	id := res.JSON201.Volume.Id
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	if err := r.c.WaitForAction(ctx, res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for volume creation", err)
		return
	}
	if plan.DeleteProtection.ValueBool() {
		if err := r.setProtection(ctx, id, true); err != nil {
			apiErr(&resp.Diagnostics, "Error enabling delete protection", err)
			return
		}
	}
	r.refresh(ctx, id, &plan, resp.State.Set, &resp.Diagnostics)
}

func (r *volumeResource) refresh(ctx context.Context, id int64, m *volumeModel, set func(context.Context, any) diagT, d *diagT) {
	g, err := client.Result(r.c.GetVolumeWithResponse(ctx, id))
	if err != nil {
		apiErr(d, "Error reading volume", err)
		return
	}
	volumeToModel(&g.JSON200.Volume, m, true)
	d.Append(set(ctx, m)...)
}

func (r *volumeResource) setProtection(ctx context.Context, id int64, on bool) error {
	_, err := client.Result(r.c.UpdateVolumeWithResponse(ctx, id, client.UpdateVolumeJSONRequestBody{
		Protection: &struct {
			Delete *bool `json:"delete,omitempty"`
		}{Delete: ptr(on)},
	}))
	return err
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid volume ID", err.Error())
		return
	}
	g, err := client.Result(r.c.GetVolumeWithResponse(ctx, id))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading volume", err)
		return
	}
	volumeToModel(&g.JSON200.Volume, &state, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())

	if !plan.Name.Equal(state.Name) || !plan.Labels.Equal(state.Labels) || !plan.DeleteProtection.Equal(state.DeleteProtection) {
		labels, d := labelsFromMap(ctx, plan.Labels)
		resp.Diagnostics.Append(d...)
		if _, err := client.Result(r.c.UpdateVolumeWithResponse(ctx, id, client.UpdateVolumeJSONRequestBody{
			Name:   ptr(plan.Name.ValueString()),
			Labels: &labels,
			Protection: &struct {
				Delete *bool `json:"delete,omitempty"`
			}{Delete: ptr(plan.DeleteProtection.ValueBool())},
		})); err != nil {
			apiErr(&resp.Diagnostics, "Error updating volume", err)
			return
		}
	}
	if plan.Size.ValueInt64() > state.Size.ValueInt64() {
		res, err := client.Result(r.c.ResizeVolumeWithResponse(ctx, id, client.ResizeVolumeJSONRequestBody{Size: int(plan.Size.ValueInt64())}))
		if err == nil {
			err = r.c.WaitForAction(ctx, &res.JSON201.Action)
		}
		if err != nil {
			apiErr(&resp.Diagnostics, "Error resizing volume", err)
			return
		}
	}
	if !plan.ServerID.IsUnknown() && !plan.ServerID.Equal(state.ServerID) {
		if !state.ServerID.IsNull() {
			if err := detachVolume(ctx, r.c, id); err != nil {
				apiErr(&resp.Diagnostics, "Error detaching volume", err)
				return
			}
		}
		if !plan.ServerID.IsNull() {
			if err := attachVolume(ctx, r.c, id, plan.ServerID.ValueInt64()); err != nil {
				apiErr(&resp.Diagnostics, "Error attaching volume", err)
				return
			}
		}
	}
	r.refresh(ctx, id, &plan, resp.State.Set, &resp.Diagnostics)
}

func attachVolume(ctx context.Context, c *client.Odinex, volumeID, serverID int64) error {
	res, err := client.Result(c.AttachVolumeWithResponse(ctx, volumeID, client.AttachVolumeJSONRequestBody{Server: serverID}))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func detachVolume(ctx context.Context, c *client.Odinex, volumeID int64) error {
	res, err := client.Result(c.DetachVolumeWithResponse(ctx, volumeID))
	if err != nil {
		return err
	}
	return c.WaitForAction(ctx, &res.JSON201.Action)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())
	g, err := client.Result(r.c.GetVolumeWithResponse(ctx, id))
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading volume", err)
		return
	}
	if g.JSON200.Volume.Server != nil {
		if err := detachVolume(ctx, r.c, id); err != nil && !client.IsNotFound(err) {
			apiErr(&resp.Diagnostics, "Error detaching volume before deletion", err)
			return
		}
	}
	if _, err := client.Result(r.c.DeleteVolumeWithResponse(ctx, id)); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error deleting volume", err)
	}
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListVolumesWithResponse(ctx, &client.ListVolumesParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing volumes", lerr)
			return
		}
		for _, v := range res.JSON200.Volumes {
			if v.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(v.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("Volume not found", fmt.Sprintf("No volume with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
