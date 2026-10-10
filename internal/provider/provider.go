// Package provider implements the Odinex Cloud Terraform provider.
package provider

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// Address is the provider's registry address.
const Address = "registry.terraform.io/odinex-cloud/odinex"

const (
	envToken        = "ODINEX_TOKEN"
	envEndpoint     = "ODINEX_ENDPOINT"
	envPollInterval = "ODINEX_POLL_INTERVAL"
)

var _ provider.Provider = (*odinexProvider)(nil)

type odinexProvider struct {
	version string
}

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &odinexProvider{version: version} }
}

type providerModel struct {
	Token        types.String `tfsdk:"token"`
	Endpoint     types.String `tfsdk:"endpoint"`
	PollInterval types.String `tfsdk:"poll_interval"`
}

func (p *odinexProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "odinex"
	resp.Version = p.version
}

func (p *odinexProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Odinex Cloud provider manages servers, networks, volumes, firewalls, " +
			"floating IPs, NAT gateways, Elastic IPs, load balancers and more through the [Odinex Cloud API](https://docs.odinex.cloud/api). " +
			"Authenticate with a project API token.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				MarkdownDescription: "Project API token (`odx_…`). Can also be set with the `ODINEX_TOKEN` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "API base URL. Defaults to `" + client.DefaultEndpoint + "`. Can also be set with `ODINEX_ENDPOINT`.",
				Optional:            true,
			},
			"poll_interval": schema.StringAttribute{
				MarkdownDescription: "How often to poll running actions, as a Go duration (e.g. `500ms`, `2s`). Defaults to `1s`. Can also be set with `ODINEX_POLL_INTERVAL`.",
				Optional:            true,
			},
		},
	}
}

func (p *odinexProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Token.IsUnknown() || cfg.Endpoint.IsUnknown() || cfg.PollInterval.IsUnknown() {
		// Values come from resources not created yet; configure on the next run.
		return
	}

	token := os.Getenv(envToken)
	if v := cfg.Token.ValueString(); v != "" {
		token = v
	}
	endpoint := os.Getenv(envEndpoint)
	if v := cfg.Endpoint.ValueString(); v != "" {
		endpoint = v
	}
	poll := os.Getenv(envPollInterval)
	if v := cfg.PollInterval.ValueString(); v != "" {
		poll = v
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing API token",
			"Set the provider's `token` argument or the ODINEX_TOKEN environment variable to a project API token.")
		return
	}
	interval := time.Second
	if poll != "" {
		d, err := time.ParseDuration(poll)
		if err != nil || d <= 0 {
			resp.Diagnostics.AddAttributeError(path.Root("poll_interval"), "Invalid poll_interval",
				fmt.Sprintf("%q is not a positive Go duration such as 500ms or 2s.", poll))
			return
		}
		interval = d
	}

	c, err := client.New(client.Config{
		Endpoint:     endpoint,
		Token:        token,
		UserAgent:    "terraform-provider-odinex/" + p.version,
		PollInterval: interval,
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the Odinex API client", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *odinexProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewServerResource,
		NewSSHKeyResource,
		NewVolumeResource,
		NewVolumeAttachmentResource,
		NewNetworkResource,
		NewServerNetworkResource,
		NewFirewallResource,
		NewFirewallAttachmentResource,
		NewFloatingIPResource,
		NewFloatingIPAssignmentResource,
		NewNatGatewayResource,
		NewElasticIPResource,
		NewElasticIPAssignmentResource,
		NewRDNSResource,
		NewLoadBalancerResource,
		NewPlacementGroupResource,
		NewSnapshotResource,
	}
}

func (p *odinexProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewLocationDataSource,
		NewLocationsDataSource,
		NewServerTypeDataSource,
		NewServerTypesDataSource,
		NewImageDataSource,
		NewSSHKeyDataSource,
		NewSSHKeysDataSource,
		NewServerDataSource,
		NewServersDataSource,
		NewNetworkDataSource,
		NewLoadBalancerTypeDataSource,
		NewLoadBalancerTypesDataSource,
		NewPricingDataSource,
	}
}
