package provider

import (
	"context"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories runs the provider in-process for acceptance tests.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"odinex": providerserver.NewProtocol6WithError(New("test")()),
}

func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("ODINEX_TOKEN") == "" {
		t.Fatal("ODINEX_TOKEN must be set for acceptance tests (and ODINEX_ENDPOINT for the dev backend)")
	}
}

func testLocation() string {
	if v := os.Getenv("ODINEX_TEST_LOCATION"); v != "" {
		return v
	}
	return "fra1"
}

// TestProviderSchemas validates every resource and data source schema the
// way Terraform does when it loads the provider.
func TestProviderSchemas(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	srv, err := providerserver.NewProtocol6WithError(p)()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("schema diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}
	wantResources := []string{
		"odinex_server", "odinex_ssh_key", "odinex_volume", "odinex_volume_attachment", "odinex_network",
		"odinex_server_network", "odinex_firewall", "odinex_firewall_attachment", "odinex_floating_ip",
		"odinex_floating_ip_assignment", "odinex_rdns", "odinex_load_balancer", "odinex_placement_group", "odinex_snapshot",
		"odinex_nat_gateway", "odinex_elastic_ip", "odinex_elastic_ip_assignment",
	}
	for _, r := range wantResources {
		if _, ok := resp.ResourceSchemas[r]; !ok {
			t.Errorf("missing resource %s", r)
		}
	}
	wantDS := []string{
		"odinex_location", "odinex_locations", "odinex_server_type", "odinex_server_types", "odinex_image",
		"odinex_ssh_key", "odinex_ssh_keys", "odinex_server", "odinex_servers", "odinex_network",
		"odinex_load_balancer_type", "odinex_load_balancer_types", "odinex_pricing",
	}
	for _, d := range wantDS {
		if _, ok := resp.DataSourceSchemas[d]; !ok {
			t.Errorf("missing data source %s", d)
		}
	}
	if len(resp.ResourceSchemas) != len(wantResources) || len(resp.DataSourceSchemas) != len(wantDS) {
		t.Errorf("got %d resources / %d data sources", len(resp.ResourceSchemas), len(resp.DataSourceSchemas))
	}

	// Every resource must support import.
	for _, f := range p.Resources(ctx) {
		r := f()
		if _, ok := r.(resource.ResourceWithImportState); !ok {
			var m resource.MetadataResponse
			r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "odinex"}, &m)
			t.Errorf("%s does not implement import", m.TypeName)
		}
	}
	for _, f := range p.DataSources(ctx) {
		if f() == nil {
			t.Error("nil data source")
		}
	}
}
