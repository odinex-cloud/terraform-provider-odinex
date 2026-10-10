package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// field describes a computed output attribute of a data source.
type field struct {
	name string
	typ  attr.Type
	desc string
}

func fieldAttrTypes(fs []field) map[string]attr.Type {
	m := make(map[string]attr.Type, len(fs))
	for _, f := range fs {
		m[f.name] = f.typ
	}
	return m
}

func computedAttr(f field) dschema.Attribute {
	switch t := f.typ.(type) {
	case types.MapType:
		return dschema.MapAttribute{Computed: true, ElementType: t.ElemType, MarkdownDescription: f.desc}
	case types.ListType:
		return dschema.ListAttribute{Computed: true, ElementType: t.ElemType, MarkdownDescription: f.desc}
	}
	switch f.typ {
	case types.Int64Type:
		return dschema.Int64Attribute{Computed: true, MarkdownDescription: f.desc}
	case types.Float64Type:
		return dschema.Float64Attribute{Computed: true, MarkdownDescription: f.desc}
	case types.BoolType:
		return dschema.BoolAttribute{Computed: true, MarkdownDescription: f.desc}
	default:
		return dschema.StringAttribute{Computed: true, MarkdownDescription: f.desc}
	}
}

// genericDataSource is a data source defined by its arguments, its output
// fields and a read function returning the output values.
type genericDataSource struct {
	c        *client.Odinex
	typeName string
	desc     string
	args     map[string]dschema.Attribute
	fields   []field
	read     func(ctx context.Context, c *client.Odinex, cfg tfsdk.Config) (map[string]attr.Value, diag.Diagnostics)
}

func (d *genericDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.typeName
}

func (d *genericDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]dschema.Attribute{}
	for _, f := range d.fields {
		attrs[f.name] = computedAttr(f)
	}
	for k, a := range d.args {
		attrs[k] = a
	}
	resp.Schema = dschema.Schema{MarkdownDescription: d.desc, Attributes: attrs}
}

func (d *genericDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.c = clientFromDataSource(req, resp)
}

func (d *genericDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	vals, diags := d.read(ctx, d.c, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.State.Raw = req.Config.Raw.Copy()
	for _, k := range sortedKeys(vals) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), vals[k])...)
	}
}

// objectList builds a list of objects of the given fields.
func objectList(fs []field, items []map[string]attr.Value) types.List {
	ot := types.ObjectType{AttrTypes: fieldAttrTypes(fs)}
	elems := make([]attr.Value, 0, len(items))
	for _, it := range items {
		elems = append(elems, types.ObjectValueMust(ot.AttrTypes, it))
	}
	return types.ListValueMust(ot, elems)
}

// ---------------------------------------------------------------- price fields

var priceFields = []field{
	{"location", types.StringType, "Location name."},
	{"price_hourly_net", types.StringType, "Hourly price without VAT (EUR)."},
	{"price_hourly_gross", types.StringType, "Hourly price with VAT (EUR)."},
	{"price_monthly_net", types.StringType, "Monthly price without VAT (EUR)."},
	{"price_monthly_gross", types.StringType, "Monthly price with VAT (EUR)."},
}

func pricesValue(ps []client.ServerTypePrice) types.List {
	items := make([]map[string]attr.Value, 0, len(ps))
	for _, p := range ps {
		items = append(items, map[string]attr.Value{
			"location":            types.StringValue(p.Location),
			"price_hourly_net":    types.StringValue(p.PriceHourly.Net),
			"price_hourly_gross":  types.StringValue(p.PriceHourly.Gross),
			"price_monthly_net":   types.StringValue(p.PriceMonthly.Net),
			"price_monthly_gross": types.StringValue(p.PriceMonthly.Gross),
		})
	}
	return objectList(priceFields, items)
}

var pricesType = types.ListType{ElemType: types.ObjectType{AttrTypes: fieldAttrTypes(priceFields)}}

// common lookup arguments
func optIDArg(desc string) dschema.Int64Attribute {
	return dschema.Int64Attribute{Optional: true, Computed: true, MarkdownDescription: desc}
}

func optNameArg(desc string) dschema.StringAttribute {
	return dschema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: desc}
}

func selectorArg() dschema.StringAttribute {
	return dschema.StringAttribute{Optional: true, MarkdownDescription: "Label selector, e.g. `env=prod,role`."}
}

func configString(ctx context.Context, cfg tfsdk.Config, name string) (types.String, diag.Diagnostics) {
	var v types.String
	d := cfg.GetAttribute(ctx, path.Root(name), &v)
	return v, d
}

func configInt64(ctx context.Context, cfg tfsdk.Config, name string) (types.Int64, diag.Diagnostics) {
	var v types.Int64
	d := cfg.GetAttribute(ctx, path.Root(name), &v)
	return v, d
}

func configBool(ctx context.Context, cfg tfsdk.Config, name string) (types.Bool, diag.Diagnostics) {
	var v types.Bool
	d := cfg.GetAttribute(ctx, path.Root(name), &v)
	return v, d
}

func configStrings(ctx context.Context, cfg tfsdk.Config, name string) ([]string, diag.Diagnostics) {
	var v types.List
	d := cfg.GetAttribute(ctx, path.Root(name), &v)
	if d.HasError() {
		return nil, d
	}
	out, d2 := stringsFrom(ctx, v)
	d.Append(d2...)
	return out, d
}

// ids helper for plural data sources
func idsAndNames(ids []int64, names []string) map[string]attr.Value {
	sids := make([]string, len(ids))
	for i, id := range ids {
		sids[i] = client.FormatID(id)
	}
	return map[string]attr.Value{"ids": stringListValue(sids), "names": stringListValue(names)}
}
