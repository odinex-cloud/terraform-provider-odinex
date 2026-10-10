package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// NewLoadBalancerResource returns the odinex_load_balancer resource.
func NewLoadBalancerResource() resource.Resource { return &loadBalancerResource{} }

type loadBalancerResource struct{ c *client.Odinex }

type loadBalancerModel struct {
	ID               types.String   `tfsdk:"id"`
	Name             types.String   `tfsdk:"name"`
	LoadBalancerType types.String   `tfsdk:"load_balancer_type"`
	Location         types.String   `tfsdk:"location"`
	NetworkID        types.Int64    `tfsdk:"network_id"`
	Labels           types.Map      `tfsdk:"labels"`
	DeleteProtection types.Bool     `tfsdk:"delete_protection"`
	Algorithm        types.Object   `tfsdk:"algorithm"`
	Service          types.List     `tfsdk:"service"`
	Target           types.Set      `tfsdk:"target"`
	IPv4             types.String   `tfsdk:"ipv4"`
	IPv6             types.String   `tfsdk:"ipv6"`
	Status           types.String   `tfsdk:"status"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

type lbServiceModel struct {
	Protocol        types.String `tfsdk:"protocol"`
	ListenPort      types.Int64  `tfsdk:"listen_port"`
	DestinationPort types.Int64  `tfsdk:"destination_port"`
	Proxyprotocol   types.Bool   `tfsdk:"proxyprotocol"`
	HTTP            types.Object `tfsdk:"http"`
	HealthCheck     types.Object `tfsdk:"health_check"`
}

type lbHealthCheckModel struct {
	Protocol types.String `tfsdk:"protocol"`
	Port     types.Int64  `tfsdk:"port"`
	Interval types.Int64  `tfsdk:"interval"`
	Timeout  types.Int64  `tfsdk:"timeout"`
	Retries  types.Int64  `tfsdk:"retries"`
	HTTP     types.Object `tfsdk:"http"`
}

type lbHealthCheckHTTPModel struct {
	Path        types.String `tfsdk:"path"`
	StatusCodes types.List   `tfsdk:"status_codes"`
}

type lbTargetModel struct {
	Type          types.String `tfsdk:"type"`
	ServerID      types.Int64  `tfsdk:"server_id"`
	LabelSelector types.String `tfsdk:"label_selector"`
	IP            types.String `tfsdk:"ip"`
}

var (
	lbAlgorithmAttrTypes   = map[string]attr.Type{"type": types.StringType}
	lbServiceHTTPAttrTypes = map[string]attr.Type{"sticky_sessions": types.BoolType}
	lbHCHTTPAttrTypes      = map[string]attr.Type{"path": types.StringType, "status_codes": types.ListType{ElemType: types.StringType}}
	lbHealthCheckAttrTypes = map[string]attr.Type{
		"protocol": types.StringType, "port": types.Int64Type, "interval": types.Int64Type,
		"timeout": types.Int64Type, "retries": types.Int64Type,
		"http": types.ObjectType{AttrTypes: lbHCHTTPAttrTypes},
	}
	lbServiceAttrTypes = map[string]attr.Type{
		"protocol": types.StringType, "listen_port": types.Int64Type, "destination_port": types.Int64Type,
		"proxyprotocol": types.BoolType,
		"http":          types.ObjectType{AttrTypes: lbServiceHTTPAttrTypes},
		"health_check":  types.ObjectType{AttrTypes: lbHealthCheckAttrTypes},
	}
	lbTargetAttrTypes = map[string]attr.Type{
		"type": types.StringType, "server_id": types.Int64Type, "label_selector": types.StringType, "ip": types.StringType,
	}
)

func (r *loadBalancerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *loadBalancerResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	port := []validator.Int64{int64validator.Between(1, 65535)}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a load balancer with its services and targets.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name of the load balancer."},
			"load_balancer_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Type of the load balancer, e.g. `lb11`. Can be changed in place.",
			},
			"location": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Location, e.g. `fra1`. Changing it recreates the load balancer.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"network_id": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "ID of a private network to reach server targets in: servers attached to it are reached on their private address " +
					"(servers without public IPs only this way). The public addresses of the load balancer do not change. Changed in place.",
			},
			"labels":            labelsAttribute(),
			"delete_protection": deleteProtectionAttribute(),
			"ipv4": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Public IPv4 address.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ipv6": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Public IPv6 address.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Status of the load balancer.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
		Blocks: map[string]schema.Block{
			"algorithm": schema.SingleNestedBlock{
				MarkdownDescription: "Load balancing algorithm. Defaults to `round_robin`.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "`round_robin` (default) or `least_connections`.",
						Validators:          []validator.String{stringvalidator.OneOf("round_robin", "least_connections")},
					},
				},
			},
			"service": schema.ListNestedBlock{
				MarkdownDescription: "A service (listener). All services are replaced in one call when any of them changes.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"protocol": schema.StringAttribute{
							Required: true, MarkdownDescription: "`tcp` or `http`.",
							Validators: []validator.String{stringvalidator.OneOf("tcp", "http")},
						},
						"listen_port": schema.Int64Attribute{Required: true, MarkdownDescription: "Port the load balancer listens on.", Validators: port},
						"destination_port": schema.Int64Attribute{
							Optional: true, MarkdownDescription: "Port of the targets; defaults to `listen_port`.", Validators: port,
						},
						"proxyprotocol": schema.BoolAttribute{Optional: true, MarkdownDescription: "Enable the PROXY protocol."},
					},
					Blocks: map[string]schema.Block{
						"http": schema.SingleNestedBlock{
							MarkdownDescription: "HTTP options.",
							Attributes: map[string]schema.Attribute{
								"sticky_sessions": schema.BoolAttribute{Optional: true, MarkdownDescription: "Enable sticky sessions."},
							},
						},
						"health_check": schema.SingleNestedBlock{
							MarkdownDescription: "Health check of the targets.",
							Attributes: map[string]schema.Attribute{
								"protocol": schema.StringAttribute{
									Optional: true, MarkdownDescription: "`tcp` or `http`.",
									Validators: []validator.String{stringvalidator.OneOf("tcp", "http")},
								},
								"port":     schema.Int64Attribute{Optional: true, MarkdownDescription: "Port to check.", Validators: port},
								"interval": schema.Int64Attribute{Optional: true, MarkdownDescription: "Seconds between checks (default 15)."},
								"timeout":  schema.Int64Attribute{Optional: true, MarkdownDescription: "Timeout in seconds (default 10)."},
								"retries":  schema.Int64Attribute{Optional: true, MarkdownDescription: "Failed checks before a target is unhealthy (default 3)."},
							},
							Blocks: map[string]schema.Block{
								"http": schema.SingleNestedBlock{
									MarkdownDescription: "HTTP health check options.",
									Attributes: map[string]schema.Attribute{
										"path":         schema.StringAttribute{Optional: true, MarkdownDescription: "Request path."},
										"status_codes": schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Accepted status codes, e.g. `[\"2??\", \"3??\"]`."},
									},
								},
							},
						},
					},
				},
			},
			"target": schema.SetNestedBlock{
				MarkdownDescription: "A target: a server, a label selector or an IP.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Required: true, MarkdownDescription: "`server`, `label_selector` or `ip`.",
							Validators: []validator.String{stringvalidator.OneOf("server", "label_selector", "ip")},
						},
						"server_id":      schema.Int64Attribute{Optional: true, MarkdownDescription: "Server ID (type `server`)."},
						"label_selector": schema.StringAttribute{Optional: true, MarkdownDescription: "Label selector (type `label_selector`)."},
						"ip":             schema.StringAttribute{Optional: true, MarkdownDescription: "IP address (type `ip`)."},
					},
				},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *loadBalancerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromResource(req, resp)
}

// ---------------------------------------------------------------- conversions

func lbAlgorithm(o types.Object) string {
	if o.IsNull() || o.IsUnknown() {
		return "round_robin"
	}
	if t, ok := o.Attributes()["type"].(types.String); ok && !t.IsNull() && !t.IsUnknown() && t.ValueString() != "" {
		return t.ValueString()
	}
	return "round_robin"
}

func lbServicesFromList(ctx context.Context, l types.List) ([]client.LBService, diag.Diagnostics) {
	out := []client.LBService{}
	if l.IsNull() || l.IsUnknown() {
		return out, nil
	}
	var ms []lbServiceModel
	diags := l.ElementsAs(ctx, &ms, false)
	for _, m := range ms {
		s := client.LBService{
			Protocol:   client.LBServiceProtocol(m.Protocol.ValueString()),
			ListenPort: int(m.ListenPort.ValueInt64()),
		}
		if v := optInt64(m.DestinationPort); v != nil {
			s.DestinationPort = ptr(int(*v))
		}
		if !m.Proxyprotocol.IsNull() && !m.Proxyprotocol.IsUnknown() {
			s.Proxyprotocol = ptr(m.Proxyprotocol.ValueBool())
		}
		if !m.HTTP.IsNull() && !m.HTTP.IsUnknown() {
			if b, ok := m.HTTP.Attributes()["sticky_sessions"].(types.Bool); ok && !b.IsNull() {
				s.Http = &struct {
					StickySessions *bool `json:"sticky_sessions,omitempty"`
				}{StickySessions: ptr(b.ValueBool())}
			}
		}
		if !m.HealthCheck.IsNull() && !m.HealthCheck.IsUnknown() {
			var hc lbHealthCheckModel
			diags.Append(m.HealthCheck.As(ctx, &hc, objAsOpts)...)
			s.HealthCheck = &struct {
				Http *struct {
					Path        *string   `json:"path,omitempty"`
					StatusCodes *[]string `json:"status_codes,omitempty"`
				} `json:"http,omitempty"`
				Interval *int                                 `json:"interval,omitempty"`
				Port     *int                                 `json:"port,omitempty"`
				Protocol *client.LBServiceHealthCheckProtocol `json:"protocol,omitempty"`
				Retries  *int                                 `json:"retries,omitempty"`
				Timeout  *int                                 `json:"timeout,omitempty"`
			}{
				Interval: intPtr(hc.Interval),
				Port:     intPtr(hc.Port),
				Retries:  intPtr(hc.Retries),
				Timeout:  intPtr(hc.Timeout),
			}
			if p := optString(hc.Protocol); p != nil {
				s.HealthCheck.Protocol = ptr(client.LBServiceHealthCheckProtocol(*p))
			}
			if !hc.HTTP.IsNull() && !hc.HTTP.IsUnknown() {
				var h lbHealthCheckHTTPModel
				diags.Append(hc.HTTP.As(ctx, &h, objAsOpts)...)
				codes, d := stringsFrom(ctx, h.StatusCodes)
				diags.Append(d...)
				s.HealthCheck.Http = &struct {
					Path        *string   `json:"path,omitempty"`
					StatusCodes *[]string `json:"status_codes,omitempty"`
				}{Path: optString(h.Path)}
				if codes != nil {
					s.HealthCheck.Http.StatusCodes = &codes
				}
			}
		}
		out = append(out, s)
	}
	return out, diags
}

func intPtr(v types.Int64) *int {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	n := int(v.ValueInt64())
	return &n
}

// keepNullInt returns null when the prior value was null (the argument was
// not configured), otherwise the API value.
func keepNullInt(prior types.Int64, api *int) types.Int64 {
	if prior.IsNull() || api == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*api))
}

func keepNullString(prior types.String, api *string) types.String {
	if prior.IsNull() || api == nil {
		return types.StringNull()
	}
	return types.StringValue(*api)
}

func keepNullBool(prior types.Bool, api *bool) types.Bool {
	if prior.IsNull() || api == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*api)
}

// lbServicesToList converts API services to blocks. Optional arguments that
// were not configured (null in prior) stay null so API defaults do not show
// up as drift; services are ordered like in prior.
func lbServicesToList(ctx context.Context, api []client.LBService, prior types.List) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	priorBy := map[int64]lbServiceModel{}
	var order []int64
	if !prior.IsNull() && !prior.IsUnknown() {
		var ps []lbServiceModel
		diags.Append(prior.ElementsAs(ctx, &ps, false)...)
		for _, p := range ps {
			priorBy[p.ListenPort.ValueInt64()] = p
			order = append(order, p.ListenPort.ValueInt64())
		}
	}
	rank := map[int64]int{}
	for i, p := range order {
		rank[p] = i
	}
	sorted := append([]client.LBService(nil), api...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, iok := rank[int64(sorted[i].ListenPort)]
		rj, jok := rank[int64(sorted[j].ListenPort)]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		default:
			return sorted[i].ListenPort < sorted[j].ListenPort
		}
	})

	elems := make([]attr.Value, 0, len(sorted))
	for _, s := range sorted {
		p, known := priorBy[int64(s.ListenPort)]
		if !known {
			// Not in config (e.g. after import): show everything the API returns.
			p = lbServiceModel{
				DestinationPort: types.Int64Value(0), Proxyprotocol: types.BoolValue(false),
				HTTP:        types.ObjectUnknown(lbServiceHTTPAttrTypes),
				HealthCheck: types.ObjectUnknown(lbHealthCheckAttrTypes),
			}
		}
		dest := keepNullInt(p.DestinationPort, s.DestinationPort)
		if !p.DestinationPort.IsNull() && s.DestinationPort == nil {
			dest = types.Int64Value(int64(s.ListenPort))
		}
		v := map[string]attr.Value{
			"protocol":         types.StringValue(string(s.Protocol)),
			"listen_port":      types.Int64Value(int64(s.ListenPort)),
			"destination_port": dest,
			"proxyprotocol":    keepNullBool(p.Proxyprotocol, s.Proxyprotocol),
			"http":             types.ObjectNull(lbServiceHTTPAttrTypes),
			"health_check":     types.ObjectNull(lbHealthCheckAttrTypes),
		}
		if !p.HTTP.IsNull() && s.Http != nil {
			prior := types.BoolValue(false)
			if !p.HTTP.IsUnknown() {
				prior, _ = p.HTTP.Attributes()["sticky_sessions"].(types.Bool)
			}
			v["http"] = types.ObjectValueMust(lbServiceHTTPAttrTypes, map[string]attr.Value{
				"sticky_sessions": keepNullBool(prior, s.Http.StickySessions),
			})
		}
		if !p.HealthCheck.IsNull() && s.HealthCheck != nil {
			ph := lbHealthCheckModel{
				Protocol: types.StringValue(""), Port: types.Int64Value(0), Interval: types.Int64Value(0),
				Timeout: types.Int64Value(0), Retries: types.Int64Value(0), HTTP: types.ObjectUnknown(lbHCHTTPAttrTypes),
			}
			if !p.HealthCheck.IsUnknown() {
				diags.Append(p.HealthCheck.As(ctx, &ph, objAsOpts)...)
			}
			hc := s.HealthCheck
			var proto *string
			if hc.Protocol != nil {
				proto = ptr(string(*hc.Protocol))
			}
			hv := map[string]attr.Value{
				"protocol": keepNullString(ph.Protocol, proto),
				"port":     keepNullInt(ph.Port, hc.Port),
				"interval": keepNullInt(ph.Interval, hc.Interval),
				"timeout":  keepNullInt(ph.Timeout, hc.Timeout),
				"retries":  keepNullInt(ph.Retries, hc.Retries),
				"http":     types.ObjectNull(lbHCHTTPAttrTypes),
			}
			if !ph.HTTP.IsNull() && hc.Http != nil {
				pp := lbHealthCheckHTTPModel{Path: types.StringValue(""), StatusCodes: types.ListUnknown(types.StringType)}
				if !ph.HTTP.IsUnknown() {
					diags.Append(ph.HTTP.As(ctx, &pp, objAsOpts)...)
				}
				codes := types.ListNull(types.StringType)
				if !pp.StatusCodes.IsNull() && hc.Http.StatusCodes != nil {
					codes = stringListValue(*hc.Http.StatusCodes)
				}
				hv["http"] = types.ObjectValueMust(lbHCHTTPAttrTypes, map[string]attr.Value{
					"path":         keepNullString(pp.Path, hc.Http.Path),
					"status_codes": codes,
				})
			}
			v["health_check"] = types.ObjectValueMust(lbHealthCheckAttrTypes, hv)
		}
		elems = append(elems, types.ObjectValueMust(lbServiceAttrTypes, v))
	}
	if len(elems) == 0 && prior.IsNull() {
		return types.ListNull(types.ObjectType{AttrTypes: lbServiceAttrTypes}), diags
	}
	l, d := types.ListValue(types.ObjectType{AttrTypes: lbServiceAttrTypes}, elems)
	diags.Append(d...)
	return l, diags
}

func lbTargetsFromSet(ctx context.Context, s types.Set) ([]client.LBTargetInput, diag.Diagnostics) {
	out := []client.LBTargetInput{}
	if s.IsNull() || s.IsUnknown() {
		return out, nil
	}
	var ms []lbTargetModel
	diags := s.ElementsAs(ctx, &ms, false)
	for _, m := range ms {
		t := client.LBTargetInput{Type: client.LBTargetInputType(m.Type.ValueString())}
		switch m.Type.ValueString() {
		case "server":
			if m.ServerID.IsNull() {
				diags.AddError("Invalid target", "A target of type `server` needs `server_id`.")
			}
			t.Server = optInt64(m.ServerID)
		case "label_selector":
			if m.LabelSelector.IsNull() {
				diags.AddError("Invalid target", "A target of type `label_selector` needs `label_selector`.")
			}
			t.LabelSelector = optString(m.LabelSelector)
		case "ip":
			if m.IP.IsNull() {
				diags.AddError("Invalid target", "A target of type `ip` needs `ip`.")
			}
			t.Ip = optString(m.IP)
		}
		out = append(out, t)
	}
	return out, diags
}

func lbTargetKey(t client.LBTargetInput) string {
	switch t.Type {
	case "server":
		if t.Server != nil {
			return fmt.Sprintf("server:%d", *t.Server)
		}
	case "ip":
		return "ip:" + derefStr(t.Ip)
	}
	return "label_selector:" + derefStr(t.LabelSelector)
}

func lbAPITargets(lb *client.LoadBalancer) []client.LBTargetInput {
	out := make([]client.LBTargetInput, 0, len(lb.Targets))
	for _, t := range lb.Targets {
		out = append(out, client.LBTargetInput{Type: client.LBTargetInputType(t.Type), Server: t.Server, LabelSelector: t.LabelSelector, Ip: t.Ip})
	}
	return out
}

func lbTargetsToSet(ts []client.LBTargetInput) types.Set {
	elems := make([]attr.Value, 0, len(ts))
	for _, t := range ts {
		v := map[string]attr.Value{
			"type": types.StringValue(string(t.Type)), "server_id": types.Int64Null(),
			"label_selector": types.StringNull(), "ip": types.StringNull(),
		}
		switch t.Type {
		case "server":
			v["server_id"] = int64PtrValue(t.Server)
		case "ip":
			v["ip"] = strPtrValue(t.Ip)
		default:
			v["label_selector"] = strPtrValue(t.LabelSelector)
		}
		elems = append(elems, types.ObjectValueMust(lbTargetAttrTypes, v))
	}
	return types.SetValueMust(types.ObjectType{AttrTypes: lbTargetAttrTypes}, elems)
}

func (r *loadBalancerResource) toModel(ctx context.Context, lb *client.LoadBalancer, m *loadBalancerModel) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(client.FormatID(lb.Id))
	m.Name = types.StringValue(lb.Name)
	m.LoadBalancerType = types.StringValue(lb.LoadBalancerType)
	m.Location = types.StringValue(lb.Location)
	m.NetworkID = int64PtrValue(lb.Network)
	m.Labels = labelsToMap(lb.Labels)
	m.DeleteProtection = types.BoolValue(lb.Protection.Delete)
	m.IPv4 = types.StringValue(derefStr(lb.PublicNet.Ipv4))
	m.IPv6 = types.StringValue(derefStr(lb.PublicNet.Ipv6))
	m.Status = types.StringValue(string(lb.Status))
	// Keep a missing algorithm block missing while the API uses the default.
	if !m.Algorithm.IsNull() || lb.Algorithm != "round_robin" {
		t := types.StringValue(string(lb.Algorithm))
		if !m.Algorithm.IsNull() && !m.Algorithm.IsUnknown() {
			if cur, ok := m.Algorithm.Attributes()["type"].(types.String); ok && cur.IsNull() && lb.Algorithm == "round_robin" {
				t = types.StringNull()
			}
		}
		m.Algorithm = types.ObjectValueMust(lbAlgorithmAttrTypes, map[string]attr.Value{"type": t})
	}
	svc, d := lbServicesToList(ctx, lb.Services, m.Service)
	diags.Append(d...)
	m.Service = svc
	targets := lbAPITargets(lb)
	if len(targets) == 0 && m.Target.IsNull() {
		m.Target = types.SetNull(types.ObjectType{AttrTypes: lbTargetAttrTypes})
	} else {
		m.Target = lbTargetsToSet(targets)
	}
	return diags
}

// ---------------------------------------------------------------- CRUD

func (r *loadBalancerResource) get(ctx context.Context, id int64) (*client.LoadBalancer, error) {
	g, err := client.Result(r.c.GetLoadBalancerWithResponse(ctx, id))
	if err != nil {
		return nil, err
	}
	return &g.JSON200.LoadBalancer, nil
}

func (r *loadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	labels, d := labelsFromMap(ctx, plan.Labels)
	resp.Diagnostics.Append(d...)
	services, d := lbServicesFromList(ctx, plan.Service)
	resp.Diagnostics.Append(d...)
	targets, d := lbTargetsFromSet(ctx, plan.Target)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	algo := client.CreateLoadBalancerJSONBodyAlgorithm(lbAlgorithm(plan.Algorithm))
	body := client.CreateLoadBalancerJSONRequestBody{
		Name:             plan.Name.ValueString(),
		LoadBalancerType: plan.LoadBalancerType.ValueString(),
		Location:         plan.Location.ValueString(),
		Algorithm:        &algo,
		Services:         &services,
		Targets:          &targets,
		Labels:           &labels,
	}
	if nid := optInt64(plan.NetworkID); nid != nil {
		body.Network = ptr(client.FormatID(*nid))
	}
	res, err := client.Result(r.c.CreateLoadBalancerWithResponse(ctx, body))
	if err != nil {
		apiErr(&resp.Diagnostics, "Error creating load balancer", err)
		return
	}
	id := res.JSON201.LoadBalancer.Id
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(id))...)
	if err := r.c.WaitForAction(ctx, &res.JSON201.Action); err != nil {
		apiErr(&resp.Diagnostics, "Error waiting for load balancer creation", err)
		return
	}
	if plan.DeleteProtection.ValueBool() {
		if err := r.update(ctx, id, nil, nil, ptr(true)); err != nil {
			apiErr(&resp.Diagnostics, "Error enabling delete protection", err)
			return
		}
	}
	lb, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading load balancer", err)
		return
	}
	resp.Diagnostics.Append(r.toModel(ctx, lb, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *loadBalancerResource) update(ctx context.Context, id int64, name *string, labels *client.Labels, protect *bool) error {
	body := client.UpdateLoadBalancerJSONRequestBody{Name: name, Labels: labels}
	if protect != nil {
		body.Protection = &struct {
			Delete *bool `json:"delete,omitempty"`
		}{Delete: protect}
	}
	_, err := client.Result(r.c.UpdateLoadBalancerWithResponse(ctx, id, body))
	return err
}

func (r *loadBalancerResource) attachNetwork(ctx context.Context, id, networkID int64) error {
	res, err := client.Result(r.c.AttachLoadBalancerToNetworkWithResponse(ctx, id, client.AttachLoadBalancerToNetworkJSONRequestBody{
		Network: client.FormatID(networkID),
	}))
	if err != nil {
		return err
	}
	return r.c.WaitForAction(ctx, &res.JSON201.Action)
}

func (r *loadBalancerResource) detachNetwork(ctx context.Context, id int64) error {
	res, err := client.Result(r.c.DetachLoadBalancerFromNetworkWithResponse(ctx, id))
	if err != nil {
		return err
	}
	return r.c.WaitForAction(ctx, &res.JSON201.Action)
}

func (r *loadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state loadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := client.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid load balancer ID", err.Error())
		return
	}
	lb, err := r.get(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading load balancer", err)
		return
	}
	resp.Diagnostics.Append(r.toModel(ctx, lb, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *loadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())

	if !plan.Name.Equal(state.Name) || !plan.Labels.Equal(state.Labels) || !plan.DeleteProtection.Equal(state.DeleteProtection) {
		labels, d := labelsFromMap(ctx, plan.Labels)
		resp.Diagnostics.Append(d...)
		if err := r.update(ctx, id, ptr(plan.Name.ValueString()), &labels, ptr(plan.DeleteProtection.ValueBool())); err != nil {
			apiErr(&resp.Diagnostics, "Error updating load balancer", err)
			return
		}
	}
	if !plan.LoadBalancerType.Equal(state.LoadBalancerType) {
		res, err := client.Result(r.c.ChangeLoadBalancerTypeWithResponse(ctx, id, client.ChangeLoadBalancerTypeJSONRequestBody{
			LoadBalancerType: plan.LoadBalancerType.ValueString(),
		}))
		if err == nil && res.JSON201 != nil {
			err = r.c.WaitForAction(ctx, &res.JSON201.Action)
		}
		if err != nil {
			apiErr(&resp.Diagnostics, "Error changing load balancer type", err)
			return
		}
	}
	// Network: detach from the old one first, then attach to the new one.
	if !plan.NetworkID.Equal(state.NetworkID) {
		if !state.NetworkID.IsNull() {
			if err := r.detachNetwork(ctx, id); err != nil && !client.IsNotFound(err) {
				apiErr(&resp.Diagnostics, "Error detaching load balancer from network", err)
				return
			}
		}
		if !plan.NetworkID.IsNull() {
			if err := r.attachNetwork(ctx, id, plan.NetworkID.ValueInt64()); err != nil {
				apiErr(&resp.Diagnostics, "Error attaching load balancer to network", err)
				return
			}
		}
	}
	if !plan.Service.Equal(state.Service) || lbAlgorithm(plan.Algorithm) != lbAlgorithm(state.Algorithm) {
		services, d := lbServicesFromList(ctx, plan.Service)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		algo := client.SetLoadBalancerServicesJSONBodyAlgorithm(lbAlgorithm(plan.Algorithm))
		res, err := client.Result(r.c.SetLoadBalancerServicesWithResponse(ctx, id, client.SetLoadBalancerServicesJSONRequestBody{
			Services: services, Algorithm: &algo,
		}))
		if err == nil {
			err = r.c.WaitForAction(ctx, &res.JSON201.Action)
		}
		if err != nil {
			apiErr(&resp.Diagnostics, "Error updating load balancer services", err)
			return
		}
	}
	if !plan.Target.Equal(state.Target) {
		want, d := lbTargetsFromSet(ctx, plan.Target)
		resp.Diagnostics.Append(d...)
		have, d := lbTargetsFromSet(ctx, state.Target)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		wantKeys, haveKeys := map[string]bool{}, map[string]bool{}
		for _, t := range want {
			wantKeys[lbTargetKey(t)] = true
		}
		for _, t := range have {
			haveKeys[lbTargetKey(t)] = true
		}
		for _, t := range have {
			if !wantKeys[lbTargetKey(t)] {
				res, err := client.Result(r.c.RemoveLoadBalancerTargetWithResponse(ctx, id, t))
				if err == nil {
					err = r.c.WaitForAction(ctx, &res.JSON201.Action)
				}
				if err != nil {
					apiErr(&resp.Diagnostics, "Error removing load balancer target", err)
					return
				}
			}
		}
		for _, t := range want {
			if !haveKeys[lbTargetKey(t)] {
				res, err := client.Result(r.c.AddLoadBalancerTargetWithResponse(ctx, id, t))
				if err == nil {
					err = r.c.WaitForAction(ctx, &res.JSON201.Action)
				}
				if err != nil {
					apiErr(&resp.Diagnostics, "Error adding load balancer target", err)
					return
				}
			}
		}
	}
	lb, err := r.get(ctx, id)
	if err != nil {
		apiErr(&resp.Diagnostics, "Error reading load balancer", err)
		return
	}
	resp.Diagnostics.Append(r.toModel(ctx, lb, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *loadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state loadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := client.ParseID(state.ID.ValueString())
	res, err := client.Result(r.c.DeleteLoadBalancerWithResponse(ctx, id))
	if client.IsNotFound(err) {
		return
	}
	if err == nil {
		err = r.c.WaitForAction(ctx, &res.JSON200.Action)
	}
	if err != nil {
		apiErr(&resp.Diagnostics, "Error deleting load balancer", err)
	}
}

func (r *loadBalancerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := client.ParseID(req.ID); err != nil {
		res, lerr := client.Result(r.c.ListLoadBalancersWithResponse(ctx, &client.ListLoadBalancersParams{}))
		if lerr != nil {
			apiErr(&resp.Diagnostics, "Error listing load balancers", lerr)
			return
		}
		for _, lb := range res.JSON200.LoadBalancers {
			if lb.Name == req.ID {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), client.FormatID(lb.Id))...)
				return
			}
		}
		resp.Diagnostics.AddError("Load balancer not found", fmt.Sprintf("No load balancer with ID or name %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
