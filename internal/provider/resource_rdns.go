package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

var (
	_ resource.ResourceWithImportState      = (*rdnsResource)(nil)
	_ resource.ResourceWithConfigValidators = (*rdnsResource)(nil)
)

// NewRDNSResource returns the odinex_rdns resource.
func NewRDNSResource() resource.Resource { return &rdnsResource{} }

type rdnsResource struct{ c *client.Odinex }

type rdnsModel struct {
	ID           types.String `tfsdk:"id"`
	ServerID     types.Int64  `tfsdk:"server_id"`
	PrimaryIPID  types.Int64  `tfsdk:"primary_ip_id"`
	FloatingIPID types.Int64  `tfsdk:"floating_ip_id"`
	IPAddress    types.String `tfsdk:"ip_address"`
	DNSPtr       types.String `tfsdk:"dns_ptr"`
}

func (r *rdnsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rdns"
}

func (r *rdnsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	forceNewID := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the reverse DNS (PTR) entry of a server's public IP, a primary IP or a floating IP. " +
			"Exactly one of `server_id`, `primary_ip_id` and `floating_ip_id` must be set. " +
			"The import ID is `s-<server_id>-<ip>`, `p-<primary_ip_id>-<ip>` or `f-<floating_ip_id>-<ip>`.",
		Attributes: map[string]schema.Attribute{
			"id":             idAttribute(),
			"server_id":      schema.Int64Attribute{Optional: true, MarkdownDescription: "ID of the server whose public IP gets the entry.", PlanModifiers: forceNewID},
			"primary_ip_id":  schema.Int64Attribute{Optional: true, MarkdownDescription: "ID of the primary IP.", PlanModifiers: forceNewID},
			"floating_ip_id": schema.Int64Attribute{Optional: true, MarkdownDescription: "ID of the floating IP.", PlanModifiers: forceNewID},
			"ip_address": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The IP address the entry is for.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"dns_ptr": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Hostname the IP resolves to. It must already resolve to the IP (forward-confirmed reverse DNS).",
			},
		},
	}
}

func (r *rdnsResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("server_id"), path.MatchRoot("primary_ip_id"), path.MatchRoot("floating_ip_id")),
	}
}

func (r *rdnsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

// rdnsTarget is the object a PTR entry belongs to.
type rdnsTarget struct {
	kind string // "s", "p" or "f"
	id   int64
}

func (m *rdnsModel) target() rdnsTarget {
	switch {
	case !m.ServerID.IsNull() && !m.ServerID.IsUnknown():
		return rdnsTarget{"s", m.ServerID.ValueInt64()}
	case !m.PrimaryIPID.IsNull() && !m.PrimaryIPID.IsUnknown():
		return rdnsTarget{"p", m.PrimaryIPID.ValueInt64()}
	default:
		return rdnsTarget{"f", m.FloatingIPID.ValueInt64()}
	}
}

func formatRDNSID(t rdnsTarget, ip string) string { return fmt.Sprintf("%s-%d-%s", t.kind, t.id, ip) }

func parseRDNSID(id string) (rdnsTarget, string, error) {
	parts := strings.SplitN(id, "-", 3)
	if len(parts) != 3 || (parts[0] != "s" && parts[0] != "p" && parts[0] != "f") {
		return rdnsTarget{}, "", fmt.Errorf("invalid rDNS ID %q: expected s-<server_id>-<ip>, p-<primary_ip_id>-<ip> or f-<floating_ip_id>-<ip>", id)
	}
	n, err := client.ParseID(parts[1])
	if err != nil {
		return rdnsTarget{}, "", err
	}
	if _, err := netip.ParseAddr(parts[2]); err != nil {
		return rdnsTarget{}, "", fmt.Errorf("invalid IP in rDNS ID %q: %w", id, err)
	}
	return rdnsTarget{parts[0], n}, parts[2], nil
}

func sameIP(a, b string) bool {
	pa, ea := netip.ParseAddr(a)
	pb, eb := netip.ParseAddr(b)
	if ea != nil || eb != nil {
		return a == b
	}
	return pa == pb
}

// lookup finds the primary/floating IP holding ip and its current PTR.
// found is false when the target or IP no longer exists.
func (r *rdnsResource) lookup(ctx context.Context, t rdnsTarget, ip string) (kind string, objID int64, ptr *string, found bool, err error) {
	switch t.kind {
	case "s":
		g, err := client.Result(r.c.GetServerWithResponse(ctx, t.id))
		if client.IsNotFound(err) {
			return "", 0, nil, false, nil
		}
		if err != nil {
			return "", 0, nil, false, err
		}
		for _, a := range []*client.PublicNetAddress{g.JSON200.Server.PublicNet.Ipv4, g.JSON200.Server.PublicNet.Ipv6} {
			if a != nil && a.Ip != nil && sameIP(*a.Ip, ip) {
				return "p", a.Id, a.DnsPtr, true, nil
			}
		}
		return "", 0, nil, false, nil
	case "p":
		g, err := client.Result(r.c.GetPrimaryIpWithResponse(ctx, t.id))
		if client.IsNotFound(err) {
			return "", 0, nil, false, nil
		}
		if err != nil {
			return "", 0, nil, false, err
		}
		p := g.JSON200.PrimaryIp
		if p.Ip == nil || !sameIP(*p.Ip, ip) {
			return "", 0, nil, false, nil
		}
		return "p", p.Id, p.DnsPtr, true, nil
	default:
		g, err := client.Result(r.c.GetFloatingIpWithResponse(ctx, t.id))
		if client.IsNotFound(err) {
			return "", 0, nil, false, nil
		}
		if err != nil {
			return "", 0, nil, false, err
		}
		f := g.JSON200.FloatingIp
		if f.Ip == nil || !sameIP(*f.Ip, ip) {
			return "", 0, nil, false, nil
		}
		return "f", f.Id, f.DnsPtr, true, nil
	}
}

// setPTR sets the entry; a nil ptr sends an explicit `{"dns_ptr": null}`
// (the generated body type would omit the field).
func (r *rdnsResource) setPTR(ctx context.Context, kind string, objID int64, ptr *string) error {
	body, err := json.Marshal(map[string]*string{"dns_ptr": ptr})
	if err != nil {
		return err
	}
	if kind == "f" {
		res, err := client.Result(r.c.ChangeFloatingIpDnsPtrWithBodyWithResponse(ctx, objID, "application/json", bytes.NewReader(body)))
		if err != nil {
			return err
		}
		return r.c.WaitForAction(ctx, &res.JSON201.Action)
	}
	res, err := client.Result(r.c.ChangePrimaryIpDnsPtrWithBodyWithResponse(ctx, objID, "application/json", bytes.NewReader(body)))
	if err != nil {
		return err
	}
	return r.c.WaitForAction(ctx, &res.JSON201.Action)
}

func (r *rdnsResource) apply(ctx context.Context, m *rdnsModel) error {
	t := m.target()
	ip := m.IPAddress.ValueString()
	kind, objID, _, found, err := r.lookup(ctx, t, ip)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("IP %s does not belong to the given %s", ip, map[string]string{"s": "server", "p": "primary IP", "f": "floating IP"}[t.kind])
	}
	if err := r.setPTR(ctx, kind, objID, ptr(m.DNSPtr.ValueString())); err != nil {
		return err
	}
	m.ID = types.StringValue(formatRDNSID(t, ip))
	return nil
}

func (r *rdnsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rdnsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiErr(&resp.Diagnostics, "Error setting reverse DNS", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *rdnsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rdnsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, ip, err := parseRDNSID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid rDNS ID", err.Error())
		return
	}
	_, _, cur, found, err := r.lookup(ctx, t, ip)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading reverse DNS", err)
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ServerID, state.PrimaryIPID, state.FloatingIPID = types.Int64Null(), types.Int64Null(), types.Int64Null()
	switch t.kind {
	case "s":
		state.ServerID = types.Int64Value(t.id)
	case "p":
		state.PrimaryIPID = types.Int64Value(t.id)
	default:
		state.FloatingIPID = types.Int64Value(t.id)
	}
	// Keep the configured spelling of the IP (e.g. IPv6 shortening).
	if state.IPAddress.IsNull() || !sameIP(state.IPAddress.ValueString(), ip) {
		state.IPAddress = types.StringValue(ip)
	}
	state.DNSPtr = types.StringValue(derefStr(cur))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *rdnsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan rdnsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiErr(&resp.Diagnostics, "Error setting reverse DNS", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *rdnsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rdnsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t := state.target()
	kind, objID, _, found, err := r.lookup(ctx, t, state.IPAddress.ValueString())
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading reverse DNS", err)
		return
	}
	if !found {
		return
	}
	// A null dns_ptr resets the entry.
	if err := r.setPTR(ctx, kind, objID, nil); err != nil && !client.IsNotFound(err) {
		apiErr(&resp.Diagnostics, "Error resetting reverse DNS", err)
	}
}

func (r *rdnsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	t, ip, err := parseRDNSID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ip_address"), ip)...)
	_ = t
}
