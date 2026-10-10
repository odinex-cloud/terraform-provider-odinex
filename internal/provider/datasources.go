package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

func pluralArgs(desc string) map[string]dschema.Attribute {
	return map[string]dschema.Attribute{
		"ids":   dschema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "IDs of all " + desc + "."},
		"names": dschema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Names of all " + desc + "."},
	}
}

// pickOne selects exactly one item by id or name.
func pickOne[T any](items []T, kind string, id types.Int64, name types.String, getID func(T) int64, getName func(T) string) (*T, diag.Diagnostics) {
	var diags diag.Diagnostics
	hasID := !id.IsNull() && !id.IsUnknown()
	hasName := !name.IsNull() && !name.IsUnknown()
	if !hasID && !hasName {
		diags.AddError("Missing argument", fmt.Sprintf("Set `id` or `name` to select a %s.", kind))
		return nil, diags
	}
	for i := range items {
		if hasID && getID(items[i]) != id.ValueInt64() {
			continue
		}
		if hasName && getName(items[i]) != name.ValueString() {
			continue
		}
		return &items[i], diags
	}
	diags.AddError(kind+" not found", fmt.Sprintf("No %s matches id=%s name=%s.", kind, id.String(), name.String()))
	return nil, diags
}

func lookupArgs(ctx context.Context, cfg tfsdk.Config) (types.Int64, types.String, diag.Diagnostics) {
	id, d := configInt64(ctx, cfg, "id")
	name, d2 := configString(ctx, cfg, "name")
	d.Append(d2...)
	return id, name, d
}

// ============================================================ locations

var locationFields = []field{
	{"id", types.Int64Type, "ID of the location."},
	{"name", types.StringType, "Name of the location, e.g. `fra1`."},
	{"description", types.StringType, "Description."},
	{"country", types.StringType, "ISO country code."},
	{"city", types.StringType, "City."},
	{"network_zone", types.StringType, "Network zone."},
	{"status", types.StringType, "`active`, `maintenance` or `soldout`."},
}

func locationValues(l client.Region) map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.Int64Value(l.Id), "name": types.StringValue(l.Name), "description": types.StringValue(l.Description),
		"country": types.StringValue(l.Country), "city": types.StringValue(l.City),
		"network_zone": types.StringValue(l.NetworkZone), "status": types.StringValue(string(l.Status)),
	}
}

func listLocations(ctx context.Context, c *client.Odinex) ([]client.Region, error) {
	var out []client.Region
	for page := 1; ; page++ {
		res, err := client.Result(c.ListRegionsWithResponse(ctx, &client.ListRegionsParams{Page: ptr(page), PerPage: ptr(50)}))
		if err != nil {
			return nil, err
		}
		out = append(out, res.JSON200.Regions...)
		if res.JSON200.Meta.Pagination.NextPage == nil {
			return out, nil
		}
	}
}

// NewLocationDataSource returns the odinex_location data source.
func NewLocationDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "location",
		desc:     "Looks up a location (region) by ID or name.",
		fields:   locationFields,
		args:     map[string]dschema.Attribute{"id": optIDArg("ID of the location."), "name": optNameArg("Name of the location.")},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			id, name, diags := lookupArgs(ctx, cfg)
			ls, err := listLocations(ctx, c)
			if err != nil {
				diags.AddError("Error listing locations", err.Error())
				return nil, diags
			}
			l, d := pickOne(ls, "location", id, name, func(r client.Region) int64 { return r.Id }, func(r client.Region) string { return r.Name })
			diags.Append(d...)
			if l == nil {
				return nil, diags
			}
			return locationValues(*l), diags
		},
	}
}

// NewLocationsDataSource returns the odinex_locations data source.
func NewLocationsDataSource() datasource.DataSource {
	args := pluralArgs("locations")
	return &genericDataSource{
		typeName: "locations",
		desc:     "Lists all locations.",
		args:     args,
		fields:   []field{{"locations", types.ListType{ElemType: types.ObjectType{AttrTypes: fieldAttrTypes(locationFields)}}, "All locations."}},
		read: func(ctx context.Context, c *client.Odinex, _ tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			var diags diag.Diagnostics
			ls, err := listLocations(ctx, c)
			if err != nil {
				diags.AddError("Error listing locations", err.Error())
				return nil, diags
			}
			var items []map[string]attr.Value
			var ids []int64
			var names []string
			for _, l := range ls {
				items = append(items, locationValues(l))
				ids = append(ids, l.Id)
				names = append(names, l.Name)
			}
			out := idsAndNames(ids, names)
			out["locations"] = objectList(locationFields, items)
			return out, diags
		},
	}
}

// ============================================================ server types

var serverTypeFields = []field{
	{"id", types.Int64Type, "ID of the server type."},
	{"name", types.StringType, "Name, e.g. `s2`."},
	{"description", types.StringType, "Description."},
	{"cores", types.Int64Type, "Number of vCPUs."},
	{"memory", types.Float64Type, "Memory in GB."},
	{"disk", types.Int64Type, "Disk in GB."},
	{"cpu_type", types.StringType, "`shared` or `dedicated`."},
	{"included_traffic", types.Int64Type, "Included traffic in bytes per month."},
	{"deprecated", types.BoolType, "Whether the type is deprecated."},
	{"prices", pricesType, "Prices per location."},
}

func serverTypeValues(t client.ServerType) map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.Int64Value(t.Id), "name": types.StringValue(t.Name), "description": types.StringValue(t.Description),
		"cores": types.Int64Value(int64(t.Cores)), "memory": types.Float64Value(float64(t.Memory)),
		"disk": types.Int64Value(int64(t.Disk)), "cpu_type": types.StringValue(string(t.CpuType)),
		"included_traffic": types.Int64Value(t.IncludedTraffic), "deprecated": types.BoolValue(t.Deprecated),
		"prices": pricesValue(t.Prices),
	}
}

func listServerTypes(ctx context.Context, c *client.Odinex) ([]client.ServerType, error) {
	var out []client.ServerType
	for page := 1; ; page++ {
		res, err := client.Result(c.ListServerTypesWithResponse(ctx, &client.ListServerTypesParams{Page: ptr(page), PerPage: ptr(50)}))
		if err != nil {
			return nil, err
		}
		out = append(out, res.JSON200.ServerTypes...)
		if res.JSON200.Meta.Pagination.NextPage == nil {
			return out, nil
		}
	}
}

// NewServerTypeDataSource returns the odinex_server_type data source.
func NewServerTypeDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "server_type",
		desc:     "Looks up a server type by ID or name.",
		fields:   serverTypeFields,
		args:     map[string]dschema.Attribute{"id": optIDArg("ID of the server type."), "name": optNameArg("Name of the server type.")},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			id, name, diags := lookupArgs(ctx, cfg)
			ts, err := listServerTypes(ctx, c)
			if err != nil {
				diags.AddError("Error listing server types", err.Error())
				return nil, diags
			}
			t, d := pickOne(ts, "server type", id, name, func(t client.ServerType) int64 { return t.Id }, func(t client.ServerType) string { return t.Name })
			diags.Append(d...)
			if t == nil {
				return nil, diags
			}
			return serverTypeValues(*t), diags
		},
	}
}

// NewServerTypesDataSource returns the odinex_server_types data source.
func NewServerTypesDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "server_types",
		desc:     "Lists all server types.",
		args:     pluralArgs("server types"),
		fields:   []field{{"server_types", types.ListType{ElemType: types.ObjectType{AttrTypes: fieldAttrTypes(serverTypeFields)}}, "All server types."}},
		read: func(ctx context.Context, c *client.Odinex, _ tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			var diags diag.Diagnostics
			ts, err := listServerTypes(ctx, c)
			if err != nil {
				diags.AddError("Error listing server types", err.Error())
				return nil, diags
			}
			var items []map[string]attr.Value
			var ids []int64
			var names []string
			for _, t := range ts {
				items = append(items, serverTypeValues(t))
				ids = append(ids, t.Id)
				names = append(names, t.Name)
			}
			out := idsAndNames(ids, names)
			out["server_types"] = objectList(serverTypeFields, items)
			return out, diags
		},
	}
}

// ============================================================ load balancer types

var lbTypeFields = []field{
	{"id", types.Int64Type, "ID of the load balancer type."},
	{"name", types.StringType, "Name, e.g. `lb11`."},
	{"description", types.StringType, "Description."},
	{"max_services", types.Int64Type, "Maximum number of services."},
	{"max_targets", types.Int64Type, "Maximum number of targets."},
	{"max_connections", types.Int64Type, "Maximum number of concurrent connections."},
	{"prices", pricesType, "Prices per location."},
}

func lbTypeValues(t client.LoadBalancerType) map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.Int64Value(t.Id), "name": types.StringValue(t.Name), "description": types.StringValue(t.Description),
		"max_services": types.Int64Value(int64(t.MaxServices)), "max_targets": types.Int64Value(int64(t.MaxTargets)),
		"max_connections": types.Int64Value(int64(t.MaxConnections)), "prices": pricesValue(t.Prices),
	}
}

// NewLoadBalancerTypeDataSource returns the odinex_load_balancer_type data source.
func NewLoadBalancerTypeDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "load_balancer_type",
		desc:     "Looks up a load balancer type by ID or name.",
		fields:   lbTypeFields,
		args:     map[string]dschema.Attribute{"id": optIDArg("ID of the load balancer type."), "name": optNameArg("Name of the load balancer type.")},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			id, name, diags := lookupArgs(ctx, cfg)
			res, err := client.Result(c.ListLoadBalancerTypesWithResponse(ctx))
			if err != nil {
				diags.AddError("Error listing load balancer types", err.Error())
				return nil, diags
			}
			t, d := pickOne(res.JSON200.LoadBalancerTypes, "load balancer type", id, name,
				func(t client.LoadBalancerType) int64 { return t.Id }, func(t client.LoadBalancerType) string { return t.Name })
			diags.Append(d...)
			if t == nil {
				return nil, diags
			}
			return lbTypeValues(*t), diags
		},
	}
}

// NewLoadBalancerTypesDataSource returns the odinex_load_balancer_types data source.
func NewLoadBalancerTypesDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "load_balancer_types",
		desc:     "Lists all load balancer types.",
		args:     pluralArgs("load balancer types"),
		fields:   []field{{"load_balancer_types", types.ListType{ElemType: types.ObjectType{AttrTypes: fieldAttrTypes(lbTypeFields)}}, "All load balancer types."}},
		read: func(ctx context.Context, c *client.Odinex, _ tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			var diags diag.Diagnostics
			res, err := client.Result(c.ListLoadBalancerTypesWithResponse(ctx))
			if err != nil {
				diags.AddError("Error listing load balancer types", err.Error())
				return nil, diags
			}
			var items []map[string]attr.Value
			var ids []int64
			var names []string
			for _, t := range res.JSON200.LoadBalancerTypes {
				items = append(items, lbTypeValues(t))
				ids = append(ids, t.Id)
				names = append(names, t.Name)
			}
			out := idsAndNames(ids, names)
			out["load_balancer_types"] = objectList(lbTypeFields, items)
			return out, diags
		},
	}
}

// ============================================================ pricing

var pricingFields = []field{
	{"sku", types.StringType, "What the price is for: `primary_ipv4`, `floating_ipv4`, `floating_ipv6`, `volume_gb`, `snapshot_gb`, `backup`, …"},
	{"unit", types.StringType, "`hour`, `gb_month` (per GB) or `percent` (share of the server price)."},
	{"location", types.StringType, "Location name."},
	{"price_hourly_net", types.StringType, "Hourly price without VAT."},
	{"price_hourly_gross", types.StringType, "Hourly price with VAT."},
	{"price_monthly_net", types.StringType, "Monthly price without VAT."},
	{"price_monthly_gross", types.StringType, "Monthly price with VAT."},
	{"currency", types.StringType, "Currency, e.g. `EUR`."},
}

// NewPricingDataSource returns the odinex_pricing data source.
func NewPricingDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "pricing",
		desc:     "Prices of resources that are not server or load balancer types (IPs, volumes, snapshots, backups), per location. Server and load balancer type prices are on `odinex_server_type(s)` and `odinex_load_balancer_type(s)`.",
		args: map[string]dschema.Attribute{
			"location": dschema.StringAttribute{Optional: true, MarkdownDescription: "Only prices of this location."},
		},
		fields: []field{{"prices", types.ListType{ElemType: types.ObjectType{AttrTypes: fieldAttrTypes(pricingFields)}}, "Prices."}},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			loc, diags := configString(ctx, cfg, "location")
			res, err := client.Result(c.GetPricingWithResponse(ctx))
			if err != nil {
				diags.AddError("Error reading pricing", err.Error())
				return nil, diags
			}
			var items []map[string]attr.Value
			for _, p := range res.JSON200.Pricing {
				if !loc.IsNull() && p.Location != loc.ValueString() {
					continue
				}
				items = append(items, map[string]attr.Value{
					"sku": types.StringValue(p.Sku), "unit": types.StringValue(string(p.Unit)), "location": types.StringValue(p.Location),
					"price_hourly_net": types.StringValue(p.PriceHourly.Net), "price_hourly_gross": types.StringValue(p.PriceHourly.Gross),
					"price_monthly_net": types.StringValue(p.PriceMonthly.Net), "price_monthly_gross": types.StringValue(p.PriceMonthly.Gross),
					"currency": types.StringValue(p.PriceHourly.Currency),
				})
			}
			return map[string]attr.Value{"prices": objectList(pricingFields, items)}, diags
		},
	}
}

// ============================================================ image

var imageFields = []field{
	{"type", types.StringType, "`system`, `app`, `snapshot` or `backup`."},
	{"description", types.StringType, "Description."},
	{"os_flavor", types.StringType, "OS flavor, e.g. `ubuntu`."},
	{"os_version", types.StringType, "OS version."},
	{"architecture", types.StringType, "Architecture, e.g. `x86`."},
	{"disk_size", types.Int64Type, "Minimum disk size in GB."},
	{"image_size", types.Float64Type, "Size in GB (snapshots)."},
	{"status", types.StringType, "Status of the image."},
	{"deprecated", types.StringType, "Deprecation date (RFC 3339), empty if not deprecated."},
	{"created", types.StringType, "Creation date (RFC 3339)."},
	{"created_from", types.Int64Type, "Server the snapshot or backup was made from."},
	{"locations", types.ListType{ElemType: types.StringType}, "Locations where the image can be used."},
	{"labels", types.MapType{ElemType: types.StringType}, "Labels."},
}

func imageValues(img client.Image) map[string]attr.Value {
	v := map[string]attr.Value{
		"id": types.Int64Value(img.Id), "name": types.StringValue(derefStr(img.Name)),
		"type": types.StringValue(string(img.Type)), "description": types.StringValue(img.Description),
		"os_flavor": types.StringValue(img.OsFlavor), "os_version": types.StringValue(derefStr(img.OsVersion)),
		"architecture": types.StringValue(img.Architecture), "disk_size": types.Int64Value(int64(img.DiskSize)),
		"image_size": types.Float64Null(), "status": types.StringValue(string(img.Status)),
		"deprecated": types.StringValue(""), "created": types.StringValue(img.Created.Format("2006-01-02T15:04:05Z07:00")),
		"created_from": int64PtrValue(img.CreatedFrom), "locations": stringListValue(img.Locations),
		"labels": labelsToMap(img.Labels),
	}
	if img.ImageSize != nil {
		v["image_size"] = types.Float64Value(float64(*img.ImageSize))
	}
	if img.Deprecated != nil {
		v["deprecated"] = types.StringValue(img.Deprecated.Format("2006-01-02T15:04:05Z07:00"))
	}
	return v
}

// filterImages applies the image data source filters and returns the
// matching images, newest first.
func filterImages(imgs []client.Image, id int64, name, selector, arch string, statuses []string, includeDeprecated bool) ([]client.Image, error) {
	var out []client.Image
	for _, img := range imgs {
		if id != 0 && img.Id != id {
			continue
		}
		if name != "" && derefStr(img.Name) != name {
			continue
		}
		if arch != "" && img.Architecture != arch {
			continue
		}
		if !includeDeprecated && img.Deprecated != nil && id == 0 {
			continue
		}
		if len(statuses) > 0 {
			ok := false
			for _, s := range statuses {
				ok = ok || string(img.Status) == s
			}
			if !ok {
				continue
			}
		}
		// The API already applied the selector; a syntax the local matcher
		// does not know is therefore treated as a match.
		if match, err := matchLabelSelector(selector, img.Labels); err == nil && !match {
			continue
		}
		out = append(out, img)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// NewImageDataSource returns the odinex_image data source.
func NewImageDataSource() datasource.DataSource {
	fields := append([]field(nil), imageFields...)
	return &genericDataSource{
		typeName: "image",
		desc: "Looks up an image (system, app, snapshot or backup) by ID, by name, or by type and label selector. " +
			"With several matches, set `most_recent = true` to pick the newest one.",
		fields: fields,
		args: map[string]dschema.Attribute{
			"id":                 optIDArg("ID of the image."),
			"name":               optNameArg("Name of the image, e.g. `ubuntu-24.04`."),
			"with_type":          dschema.StringAttribute{Optional: true, MarkdownDescription: "Only images of this type: `system`, `app`, `snapshot` or `backup`."},
			"with_selector":      selectorArg(),
			"with_status":        dschema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Only images with one of these statuses, e.g. `[\"available\"]`."},
			"with_architecture":  dschema.StringAttribute{Optional: true, MarkdownDescription: "Only images of this architecture, e.g. `x86`."},
			"location":           dschema.StringAttribute{Optional: true, MarkdownDescription: "Only images available in this location."},
			"most_recent":        dschema.BoolAttribute{Optional: true, MarkdownDescription: "Pick the newest image when several match."},
			"include_deprecated": dschema.BoolAttribute{Optional: true, MarkdownDescription: "Also consider deprecated images."},
		},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			id, name, diags := lookupArgs(ctx, cfg)
			typ, d := configString(ctx, cfg, "with_type")
			diags.Append(d...)
			sel, d := configString(ctx, cfg, "with_selector")
			diags.Append(d...)
			arch, d := configString(ctx, cfg, "with_architecture")
			diags.Append(d...)
			loc, d := configString(ctx, cfg, "location")
			diags.Append(d...)
			mostRecent, d := configBool(ctx, cfg, "most_recent")
			diags.Append(d...)
			inclDep, d := configBool(ctx, cfg, "include_deprecated")
			diags.Append(d...)
			statuses, d := configStrings(ctx, cfg, "with_status")
			diags.Append(d...)
			if diags.HasError() {
				return nil, diags
			}
			if id.IsNull() && name.IsNull() && sel.IsNull() && typ.IsNull() {
				diags.AddError("Missing argument", "Set `id`, `name`, `with_type` or `with_selector`.")
				return nil, diags
			}
			// name and label_selector are filtered by the API; the same filters
			// run again locally (harmless, and covers older API versions).
			params := &client.ListImagesParams{Location: optString(loc), Name: optString(name), LabelSelector: optString(sel)}
			if t := optString(typ); t != nil {
				params.Type = ptr(client.ListImagesParamsType(*t))
			}
			res, err := client.Result(c.ListImagesWithResponse(ctx, params))
			if err != nil {
				diags.AddError("Error listing images", err.Error())
				return nil, diags
			}
			imgs, err := filterImages(res.JSON200.Images, id.ValueInt64(), name.ValueString(), sel.ValueString(), arch.ValueString(), statuses, inclDep.ValueBool())
			if err != nil {
				diags.AddError("Invalid label selector", err.Error())
				return nil, diags
			}
			switch {
			case len(imgs) == 0:
				diags.AddError("Image not found", "No image matches the given arguments.")
				return nil, diags
			case len(imgs) > 1 && !mostRecent.ValueBool():
				names := make([]string, 0, len(imgs))
				for _, i := range imgs {
					names = append(names, fmt.Sprintf("%d (%s)", i.Id, derefStr(i.Name)))
				}
				diags.AddError("More than one image found", "Matching images: "+strings.Join(names, ", ")+". Narrow the arguments or set `most_recent = true`.")
				return nil, diags
			}
			return imageValues(imgs[0]), diags
		},
	}
}

// ============================================================ ssh keys

var sshKeyFields = []field{
	{"id", types.Int64Type, "ID of the SSH key."},
	{"name", types.StringType, "Name of the SSH key."},
	{"fingerprint", types.StringType, "Fingerprint."},
	{"public_key", types.StringType, "Public key."},
	{"labels", types.MapType{ElemType: types.StringType}, "Labels."},
}

func sshKeyValues(k client.SshKey) map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.Int64Value(k.Id), "name": types.StringValue(k.Name), "fingerprint": types.StringValue(k.Fingerprint),
		"public_key": types.StringValue(k.PublicKey), "labels": labelsToMap(k.Labels),
	}
}

// NewSSHKeyDataSource returns the odinex_ssh_key data source.
func NewSSHKeyDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "ssh_key",
		desc:     "Looks up an SSH key by ID, name, fingerprint or label selector.",
		fields:   sshKeyFields,
		args: map[string]dschema.Attribute{
			"id": optIDArg("ID of the SSH key."), "name": optNameArg("Name of the SSH key."),
			"fingerprint":   dschema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Fingerprint of the SSH key."},
			"with_selector": selectorArg(),
		},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			id, name, diags := lookupArgs(ctx, cfg)
			fp, d := configString(ctx, cfg, "fingerprint")
			diags.Append(d...)
			sel, d := configString(ctx, cfg, "with_selector")
			diags.Append(d...)
			if id.IsNull() && name.IsNull() && fp.IsNull() && sel.IsNull() {
				diags.AddError("Missing argument", "Set `id`, `name`, `fingerprint` or `with_selector`.")
				return nil, diags
			}
			res, err := client.Result(c.ListSshKeysWithResponse(ctx, &client.ListSshKeysParams{LabelSelector: optString(sel)}))
			if err != nil {
				diags.AddError("Error listing SSH keys", err.Error())
				return nil, diags
			}
			var found []client.SshKey
			for _, k := range res.JSON200.SshKeys {
				if (!id.IsNull() && k.Id != id.ValueInt64()) || (!name.IsNull() && k.Name != name.ValueString()) ||
					(!fp.IsNull() && k.Fingerprint != fp.ValueString()) {
					continue
				}
				found = append(found, k)
			}
			if len(found) != 1 {
				diags.AddError("SSH key not found", fmt.Sprintf("Expected exactly one SSH key, found %d.", len(found)))
				return nil, diags
			}
			return sshKeyValues(found[0]), diags
		},
	}
}

// NewSSHKeysDataSource returns the odinex_ssh_keys data source.
func NewSSHKeysDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "ssh_keys",
		desc:     "Lists SSH keys, optionally filtered by a label selector.",
		args:     map[string]dschema.Attribute{"with_selector": selectorArg()},
		fields:   []field{{"ssh_keys", types.ListType{ElemType: types.ObjectType{AttrTypes: fieldAttrTypes(sshKeyFields)}}, "Matching SSH keys."}},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			sel, diags := configString(ctx, cfg, "with_selector")
			res, err := client.Result(c.ListSshKeysWithResponse(ctx, &client.ListSshKeysParams{LabelSelector: optString(sel)}))
			if err != nil {
				diags.AddError("Error listing SSH keys", err.Error())
				return nil, diags
			}
			var items []map[string]attr.Value
			for _, k := range res.JSON200.SshKeys {
				items = append(items, sshKeyValues(k))
			}
			return map[string]attr.Value{"ssh_keys": objectList(sshKeyFields, items)}, diags
		},
	}
}

// ============================================================ servers

var serverNetworkFieldTypes = map[string]attr.Type{"network_id": types.Int64Type, "ip": types.StringType}

var serverFields = []field{
	{"id", types.Int64Type, "ID of the server."},
	{"name", types.StringType, "Name of the server."},
	{"server_type", types.StringType, "Name of the server type."},
	{"image", types.StringType, "Name (or ID) of the image."},
	{"location", types.StringType, "Location name."},
	{"status", types.StringType, "Status."},
	{"ipv4_address", types.StringType, "Public IPv4 address."},
	{"ipv6_address", types.StringType, "Public IPv6 address."},
	{"labels", types.MapType{ElemType: types.StringType}, "Labels."},
	{"backups", types.BoolType, "Whether backups are enabled."},
	{"backup_window", types.StringType, "Backup window."},
	{"placement_group_id", types.Int64Type, "Placement group ID."},
	{"delete_protection", types.BoolType, "Whether delete protection is on."},
	{"primary_disk_size", types.Int64Type, "Disk size in GB."},
	{"rescue_enabled", types.BoolType, "Whether the rescue system is enabled."},
	{"network", types.ListType{ElemType: types.ObjectType{AttrTypes: serverNetworkFieldTypes}}, "Private networks (`network_id`, `ip`)."},
	{"primary_network_id", types.Int64Type, "Network a server without public IPs lives on."},
	{"elastic_ip_id", types.Int64Type, "ID of the Elastic IP of a server without public IPs."},
	{"elastic_ip_address", types.StringType, "Address of the Elastic IP (empty without one)."},
	{"volumes", types.ListType{ElemType: types.Int64Type}, "IDs of attached volumes."},
	{"floating_ips", types.ListType{ElemType: types.Int64Type}, "IDs of assigned floating IPs."},
}

func serverValues(s client.Server) map[string]attr.Value {
	img := ""
	if s.Image != nil {
		img = derefStr(s.Image.Name)
		if img == "" {
			img = client.FormatID(s.Image.Id)
		}
	}
	v4, v6 := "", ""
	if s.PublicNet.Ipv4 != nil {
		v4 = derefStr(s.PublicNet.Ipv4.Ip)
	}
	if s.PublicNet.Ipv6 != nil {
		v6 = derefStr(s.PublicNet.Ipv6.Ip)
	}
	eipID, eipIP := types.Int64Null(), ""
	if e := s.PublicNet.ElasticIp; e != nil {
		eipID, eipIP = types.Int64Value(e.Id), derefStr(e.Ip)
	}
	nets := make([]attr.Value, 0, len(s.PrivateNet))
	for _, n := range s.PrivateNet {
		nets = append(nets, types.ObjectValueMust(serverNetworkFieldTypes, map[string]attr.Value{
			"network_id": types.Int64Value(n.Network), "ip": types.StringValue(derefStr(n.Ip)),
		}))
	}
	return map[string]attr.Value{
		"id": types.Int64Value(s.Id), "name": types.StringValue(s.Name), "server_type": types.StringValue(s.ServerType.Name),
		"image": types.StringValue(img), "location": types.StringValue(s.Location.Name), "status": types.StringValue(string(s.Status)),
		"ipv4_address": types.StringValue(v4), "ipv6_address": types.StringValue(v6), "labels": labelsToMap(s.Labels),
		"backups": types.BoolValue(s.BackupWindow != nil), "backup_window": types.StringValue(derefStr(s.BackupWindow)),
		"placement_group_id": int64PtrValue(s.PlacementGroup), "delete_protection": types.BoolValue(s.Protection.Delete),
		"primary_disk_size": types.Int64Value(int64(s.PrimaryDiskSize)), "rescue_enabled": types.BoolValue(s.RescueEnabled),
		"network":            types.ListValueMust(types.ObjectType{AttrTypes: serverNetworkFieldTypes}, nets),
		"primary_network_id": int64PtrValue(s.PrimaryNetwork),
		"elastic_ip_id":      eipID,
		"elastic_ip_address": types.StringValue(eipIP),
		"volumes":            int64ListValue(s.Volumes),
		"floating_ips":       int64ListValue(s.FloatingIps),
	}
}

func listServers(ctx context.Context, c *client.Odinex, params client.ListServersParams) ([]client.Server, error) {
	var out []client.Server
	for page := 1; ; page++ {
		params.Page, params.PerPage = ptr(page), ptr(50)
		res, err := client.Result(c.ListServersWithResponse(ctx, &params))
		if err != nil {
			return nil, err
		}
		out = append(out, res.JSON200.Servers...)
		if res.JSON200.Meta.Pagination.NextPage == nil {
			return out, nil
		}
	}
}

// NewServerDataSource returns the odinex_server data source.
func NewServerDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "server",
		desc:     "Looks up a server by ID, name or label selector.",
		fields:   serverFields,
		args: map[string]dschema.Attribute{
			"id": optIDArg("ID of the server."), "name": optNameArg("Name of the server."),
			"with_selector": selectorArg(),
			"with_status":   dschema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Only servers with one of these statuses."},
		},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			id, name, diags := lookupArgs(ctx, cfg)
			sel, d := configString(ctx, cfg, "with_selector")
			diags.Append(d...)
			statuses, d := configStrings(ctx, cfg, "with_status")
			diags.Append(d...)
			if diags.HasError() {
				return nil, diags
			}
			if !id.IsNull() {
				res, err := client.Result(c.GetServerWithResponse(ctx, id.ValueInt64()))
				if err != nil {
					diags.AddError("Error reading server", err.Error())
					return nil, diags
				}
				return serverValues(res.JSON200.Server), diags
			}
			if name.IsNull() && sel.IsNull() {
				diags.AddError("Missing argument", "Set `id`, `name` or `with_selector`.")
				return nil, diags
			}
			ss, err := listServers(ctx, c, client.ListServersParams{Name: optString(name), LabelSelector: optString(sel)})
			if err != nil {
				diags.AddError("Error listing servers", err.Error())
				return nil, diags
			}
			ss = filterServerStatus(ss, statuses)
			if len(ss) != 1 {
				diags.AddError("Server not found", fmt.Sprintf("Expected exactly one server, found %d.", len(ss)))
				return nil, diags
			}
			return serverValues(ss[0]), diags
		},
	}
}

func filterServerStatus(ss []client.Server, statuses []string) []client.Server {
	if len(statuses) == 0 {
		return ss
	}
	var out []client.Server
	for _, s := range ss {
		for _, st := range statuses {
			if string(s.Status) == st {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// NewServersDataSource returns the odinex_servers data source.
func NewServersDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "servers",
		desc:     "Lists servers, optionally filtered by label selector and status.",
		args: map[string]dschema.Attribute{
			"with_selector": selectorArg(),
			"with_statuses": dschema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Only servers with one of these statuses."},
		},
		fields: []field{{"servers", types.ListType{ElemType: types.ObjectType{AttrTypes: fieldAttrTypes(serverFields)}}, "Matching servers."}},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			sel, diags := configString(ctx, cfg, "with_selector")
			statuses, d := configStrings(ctx, cfg, "with_statuses")
			diags.Append(d...)
			ss, err := listServers(ctx, c, client.ListServersParams{LabelSelector: optString(sel)})
			if err != nil {
				diags.AddError("Error listing servers", err.Error())
				return nil, diags
			}
			var items []map[string]attr.Value
			for _, s := range filterServerStatus(ss, statuses) {
				items = append(items, serverValues(s))
			}
			return map[string]attr.Value{"servers": objectList(serverFields, items)}, diags
		},
	}
}

// ============================================================ network

var networkFields = []field{
	{"id", types.Int64Type, "ID of the network."},
	{"name", types.StringType, "Name of the network."},
	{"ip_range", types.StringType, "IP range."},
	{"location", types.StringType, "Location."},
	{"labels", types.MapType{ElemType: types.StringType}, "Labels."},
	{"delete_protection", types.BoolType, "Whether delete protection is on."},
	{"servers", types.ListType{ElemType: types.Int64Type}, "IDs of attached servers."},
	{"gateway", types.StringType, "Address kept for a NAT gateway: the first host of the range."},
	{"nat_gateway_id", types.Int64Type, "ID of the network's NAT gateway, if any."},
}

// NewNetworkDataSource returns the odinex_network data source.
func NewNetworkDataSource() datasource.DataSource {
	return &genericDataSource{
		typeName: "network",
		desc:     "Looks up a private network by ID, name or label selector.",
		fields:   networkFields,
		args: map[string]dschema.Attribute{
			"id": optIDArg("ID of the network."), "name": optNameArg("Name of the network."), "with_selector": selectorArg(),
		},
		read: func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics) {
			id, name, diags := lookupArgs(ctx, cfg)
			sel, d := configString(ctx, cfg, "with_selector")
			diags.Append(d...)
			if id.IsNull() && name.IsNull() && sel.IsNull() {
				diags.AddError("Missing argument", "Set `id`, `name` or `with_selector`.")
				return nil, diags
			}
			res, err := client.Result(c.ListNetworksWithResponse(ctx, &client.ListNetworksParams{LabelSelector: optString(sel)}))
			if err != nil {
				diags.AddError("Error listing networks", err.Error())
				return nil, diags
			}
			var found []client.Network
			for _, n := range res.JSON200.Networks {
				if (!id.IsNull() && n.Id != id.ValueInt64()) || (!name.IsNull() && n.Name != name.ValueString()) {
					continue
				}
				found = append(found, n)
			}
			if len(found) != 1 {
				diags.AddError("Network not found", fmt.Sprintf("Expected exactly one network, found %d.", len(found)))
				return nil, diags
			}
			n := found[0]
			servers := make([]int64, 0, len(n.Servers))
			for _, s := range n.Servers {
				servers = append(servers, s.Server)
			}
			return map[string]attr.Value{
				"id": types.Int64Value(n.Id), "name": types.StringValue(n.Name), "ip_range": types.StringValue(n.IpRange),
				"location": types.StringValue(n.Location), "labels": labelsToMap(n.Labels),
				"delete_protection": types.BoolValue(n.Protection.Delete), "servers": int64ListValue(servers),
				"gateway": types.StringValue(n.Gateway), "nat_gateway_id": int64PtrValue(n.NatGateway),
			}, diags
		},
	}
}
