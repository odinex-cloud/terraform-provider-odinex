package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

var _ resource.ResourceWithImportState = (*sshKeyResource)(nil)

// NewSSHKeyResource returns the odinex_ssh_key resource.
func NewSSHKeyResource() resource.Resource { return &sshKeyResource{} }

type sshKeyResource struct{ c *client.Odinex }

type sshKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	Labels      types.Map    `tfsdk:"labels"`
}

func (r *sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an SSH key of the project.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name of the SSH key."},
			"public_key": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Public key in OpenSSH format. Changing it recreates the key.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"fingerprint": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Fingerprint of the key.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"labels": labelsAttribute(),
		},
	}
}

func (r *sshKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

func sshKeyToModel(k *client.SshKey, m *sshKeyModel) {
	m.ID = types.StringValue(client.FormatID(k.Id))
	m.Name = types.StringValue(k.Name)
	// Keep the configured spelling when only whitespace differs.
	if m.PublicKey.IsNull() || m.PublicKey.IsUnknown() || strings.TrimSpace(m.PublicKey.ValueString()) != strings.TrimSpace(k.PublicKey) {
		m.PublicKey = types.StringValue(k.PublicKey)
	}
	m.Fingerprint = types.StringValue(k.Fingerprint)
	m.Labels = labelsToMap(k.Labels)
}

// findSSHKey looks a key up by ID. It returns nil when the key does not exist.
func findSSHKey(ctx context.Context, c *client.Odinex, id int64) (*client.SshKey, error) {
	res, err := client.Result(c.GetSshKeyWithResponse(ctx, id))
	if client.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &res.JSON200.SshKey, nil
}

func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := client.Result(r.c.CreateSshKeyWithResponse(ctx, client.CreateSshKeyJSONRequestBody{
		Name:      plan.Name.ValueString(),
		PublicKey: plan.PublicKey.ValueString(),
		Labels:    &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating SSH key", err)
		return
	}
	sshKeyToModel(&res.JSON201.SshKey, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid SSH key ID", err.Error())
		return
	}
	k, err := findSSHKey(ctx, r.c, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading SSH key", err)
		return
	}
	if k == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	sshKeyToModel(k, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sshKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	res, err := client.Result(r.c.UpdateSshKeyWithResponse(ctx, id, client.UpdateSshKeyJSONRequestBody{
		Name:   ptr(plan.Name.ValueString()),
		Labels: &labels,
	}))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error updating SSH key", err)
		return
	}
	sshKeyToModel(&res.JSON200.SshKey, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := client.ParseID(state.ID.ValueString())
	if _, err := client.Result(r.c.DeleteSshKeyWithResponse(ctx, id)); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error deleting SSH key", err)
	}
}

func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		// Import by name.
		res, lerr := client.Result(r.c.ListSshKeysWithResponse(ctx, &client.ListSshKeysParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing SSH keys", lerr)
			return
		}
		for _, k := range res.JSON200.SshKeys {
			if k.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(k.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("SSH key not found", fmt.Sprintf("No SSH key with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
