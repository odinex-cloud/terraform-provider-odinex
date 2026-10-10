package provider

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

// Acceptance tests create real resources. Run them against the dev backend:
//
//	TF_ACC=1 ODINEX_ENDPOINT=http://localhost:18090/v1 ODINEX_TOKEN=odx_... go test ./internal/provider -run TestAcc -v

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGNA5kG6x3+5Vb7mE5uYbqgV0R2Fx7n6Qn5X1KJ8r3cT tf-acc@example"

func accName() string { return acctest.RandomWithPrefix("tf-acc") }

func TestAccSSHKey(t *testing.T) {
	name := accName()
	cfg := func(n, env string) string {
		return fmt.Sprintf(`
resource "odinex_ssh_key" "k" {
  name       = %q
  public_key = %q
  labels     = { env = %q }
}`, n, testKey, env)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(name, "test"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_ssh_key.k", "name", name),
					resource.TestCheckResourceAttr("odinex_ssh_key.k", "labels.env", "test"),
					resource.TestMatchResourceAttr("odinex_ssh_key.k", "fingerprint", regexp.MustCompile(`.+`)),
				),
			},
			{ResourceName: "odinex_ssh_key.k", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_ssh_key.k", ImportState: true, ImportStateVerify: true, ImportStateId: name},
			{
				Config: cfg(name+"-renamed", "prod"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_ssh_key.k", "name", name+"-renamed"),
					resource.TestCheckResourceAttr("odinex_ssh_key.k", "labels.env", "prod"),
				),
			},
		},
	})
}

// TestAccServerFull covers networks (block), placement groups, backups,
// labels, protection, volume grow/attach, change type (keep_disk) and rebuild.
func TestAccServerFull(t *testing.T) {
	name := accName()
	cfg := func(serverType, image string, keepDisk, backups bool, env string, size int, withNet bool) string {
		net := ""
		if withNet {
			net = `
  network {
    network_id = odinex_network.n.id
    ip         = "10.20.0.5"
  }`
		}
		return fmt.Sprintf(`
resource "odinex_network" "n" {
  name     = "%[1]s"
  ip_range = "10.20.0.0/16"
  location = %[2]q
  labels   = { env = %[7]q }
}

resource "odinex_placement_group" "pg" {
  name     = "%[1]s"
  location = %[2]q
  labels   = { env = %[7]q }
}

resource "odinex_server" "s" {
  name               = "%[1]s"
  server_type        = %[3]q
  image              = %[4]q
  location           = %[2]q
  placement_group_id = odinex_placement_group.pg.id
  keep_disk          = %[5]t
  backups            = %[6]t
  user_data          = "#cloud-config\nruncmd: [echo hi]\n"
  labels             = { env = %[7]q, role = "acc" }
  %[9]s
}

resource "odinex_volume" "v" {
  name      = "%[1]s"
  size      = %[8]d
  server_id = odinex_server.s.id
  labels    = { env = %[7]q }
}
`, name, testLocation(), serverType, image, keepDisk, backups, env, size, net)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("t1", "ubuntu-24.04", true, true, "a", 10, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_server.s", "status", "running"),
					resource.TestCheckResourceAttr("odinex_server.s", "backups", "true"),
					resource.TestMatchResourceAttr("odinex_server.s", "backup_window", regexp.MustCompile(`.+`)),
					resource.TestMatchResourceAttr("odinex_server.s", "ipv4_address", regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)),
					resource.TestMatchResourceAttr("odinex_server.s", "ipv6_address", regexp.MustCompile(`:`)),
					resource.TestCheckResourceAttr("odinex_server.s", "network.#", "1"),
					resource.TestCheckResourceAttr("odinex_server.s", "network.0.ip", "10.20.0.5"),
					resource.TestCheckResourceAttrPair("odinex_server.s", "placement_group_id", "odinex_placement_group.pg", "id"),
					resource.TestCheckResourceAttrPair("odinex_volume.v", "server_id", "odinex_server.s", "id"),
					resource.TestCheckResourceAttr("odinex_volume.v", "location", testLocation()),
					resource.TestMatchResourceAttr("odinex_volume.v", "linux_device", regexp.MustCompile(`^/dev/`)),
				),
			},
			{
				ResourceName:            "odinex_server.s",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"user_data", "ssh_keys", "keep_disk", "shutdown_before_deletion", "network", "public_net", "timeouts"},
			},
			{ResourceName: "odinex_server.s", ImportState: true, ImportStateId: name, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"user_data", "ssh_keys", "keep_disk", "shutdown_before_deletion", "network", "public_net", "timeouts"}},
			{ResourceName: "odinex_network.n", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "odinex_placement_group.pg", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"servers"}}, // servers changes when the server joins
			{ResourceName: "odinex_volume.v", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				// Change type keeping the disk, change labels, disable backups,
				// grow the volume, detach from the network.
				Config: cfg("t2", "ubuntu-24.04", true, false, "b", 20, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_server.s", "server_type", "t2"),
					resource.TestCheckResourceAttr("odinex_server.s", "primary_disk_size", "40"),
					resource.TestCheckResourceAttr("odinex_server.s", "status", "running"),
					resource.TestCheckResourceAttr("odinex_server.s", "backups", "false"),
					resource.TestCheckResourceAttr("odinex_server.s", "labels.env", "b"),
					resource.TestCheckResourceAttr("odinex_server.s", "network.#", "0"),
					resource.TestCheckResourceAttr("odinex_volume.v", "size", "20"),
				),
			},
			{
				// Upgrade with disk and rebuild with another image.
				Config: cfg("t3", "debian-13", false, false, "b", 20, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_server.s", "server_type", "t3"),
					resource.TestCheckResourceAttr("odinex_server.s", "primary_disk_size", "80"),
					resource.TestCheckResourceAttr("odinex_server.s", "image", "debian-13"),
				),
			},
		},
	})
}

// TestAccAttachments covers the stand-alone attachment resources.
func TestAccAttachments(t *testing.T) {
	name := accName()
	cfg := func(attach bool) string {
		att := ""
		if attach {
			att = `
resource "odinex_server_network" "sn" {
  server_id  = odinex_server.s.id
  network_id = odinex_network.n.id
}

resource "odinex_volume_attachment" "va" {
  volume_id = odinex_volume.v.id
  server_id = odinex_server.s.id
}

resource "odinex_floating_ip_assignment" "fa" {
  floating_ip_id = odinex_floating_ip.f.id
  server_id      = odinex_server.s.id
}

resource "odinex_firewall_attachment" "fwa" {
  firewall_id     = odinex_firewall.fw.id
  server_ids      = [odinex_server.s.id]
  label_selectors = ["role=acc-att"]
}
`
		}
		return fmt.Sprintf(`
resource "odinex_network" "n" {
  name     = "%[1]s"
  ip_range = "10.30.0.0/16"
  location = %[2]q
}

resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q
  labels      = { role = "acc-att" }
}

resource "odinex_volume" "v" {
  name     = "%[1]s"
  size     = 10
  location = %[2]q
}

resource "odinex_floating_ip" "f" {
  type          = "ipv6"
  name          = "%[1]s"
  description   = "acc test"
  home_location = %[2]q
}

resource "odinex_firewall" "fw" {
  name = "%[1]s"
  rule {
    direction       = "out"
    protocol        = "udp"
    port            = "53"
    destination_ips = ["0.0.0.0/0"]
  }
}
%[3]s`, name, testLocation(), att)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("odinex_server_network.sn", "ip", regexp.MustCompile(`^10\.30\.`)),
					resource.TestCheckResourceAttrPair("odinex_volume_attachment.va", "id", "odinex_volume.v", "id"),
					resource.TestCheckResourceAttrPair("odinex_floating_ip_assignment.fa", "id", "odinex_floating_ip.f", "id"),
					resource.TestCheckResourceAttr("odinex_firewall_attachment.fwa", "server_ids.#", "1"),
					resource.TestCheckResourceAttr("odinex_firewall_attachment.fwa", "label_selectors.#", "1"),
				),
			},
			{ResourceName: "odinex_server_network.sn", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_volume_attachment.va", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_floating_ip_assignment.fa", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_firewall_attachment.fwa", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_floating_ip.f", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "server_id"}}, // assigned by odinex_floating_ip_assignment after creation
			{ResourceName: "odinex_firewall.fw", ImportState: true, ImportStateVerify: true, ImportStateId: name},
			{
				// Removing the attachments detaches everything again.
				Config: cfg(false),
			},
			{
				// server_id is Optional+Computed, so it is updated by a refresh.
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("odinex_volume.v", "server_id"),
					resource.TestCheckNoResourceAttr("odinex_floating_ip.f", "server_id"),
					resource.TestCheckNoResourceAttr("odinex_firewall.fw", "apply_to.#"),
				),
			},
		},
	})
}

// TestAccFloatingIPAndRDNS covers floating IPs with server_id and all rDNS variants.
func TestAccFloatingIPAndRDNS(t *testing.T) {
	name := accName()
	cfg := func(ptr string, protect bool) string {
		return fmt.Sprintf(`
resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q
}

resource "odinex_floating_ip" "f" {
  type              = "ipv4"
  server_id         = odinex_server.s.id
  labels            = { a = "b" }
  delete_protection = %[4]t
}

resource "odinex_rdns" "server" {
  server_id  = odinex_server.s.id
  ip_address = odinex_server.s.ipv4_address
  dns_ptr    = "server.%[3]s"
}

resource "odinex_rdns" "primary" {
  primary_ip_id = odinex_server.s.primary_ipv6_id
  ip_address    = odinex_server.s.ipv6_address
  dns_ptr       = "v6.%[3]s"
}

resource "odinex_rdns" "floating" {
  floating_ip_id = odinex_floating_ip.f.id
  ip_address     = odinex_floating_ip.f.ip_address
  dns_ptr        = "fip.%[3]s"
}
`, name, testLocation(), ptr, protect)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("example.com", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("odinex_floating_ip.f", "server_id", "odinex_server.s", "id"),
					resource.TestCheckResourceAttr("odinex_floating_ip.f", "home_location", testLocation()),
					resource.TestCheckResourceAttr("odinex_floating_ip.f", "delete_protection", "true"),
					resource.TestCheckResourceAttr("odinex_rdns.server", "dns_ptr", "server.example.com"),
					resource.TestMatchResourceAttr("odinex_rdns.server", "id", regexp.MustCompile(`^s-\d+-`)),
					resource.TestMatchResourceAttr("odinex_rdns.primary", "id", regexp.MustCompile(`^p-\d+-`)),
					resource.TestMatchResourceAttr("odinex_rdns.floating", "id", regexp.MustCompile(`^f-\d+-`)),
				),
			},
			{ResourceName: "odinex_rdns.server", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_rdns.primary", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_rdns.floating", ImportState: true, ImportStateVerify: true},
			{
				Config: cfg("example.org", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_rdns.server", "dns_ptr", "server.example.org"),
					resource.TestCheckResourceAttr("odinex_rdns.floating", "dns_ptr", "fip.example.org"),
					resource.TestCheckResourceAttr("odinex_floating_ip.f", "delete_protection", "false"),
				),
			},
		},
	})
}

func TestAccLoadBalancer(t *testing.T) {
	name := accName()
	step1 := fmt.Sprintf(`
resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q
  labels      = { lb = "%[1]s" }
}

resource "odinex_load_balancer" "lb" {
  name               = "%[1]s"
  load_balancer_type = "lb11"
  location           = %[2]q

  service {
    protocol    = "tcp"
    listen_port = 443
  }

  target {
    type      = "server"
    server_id = odinex_server.s.id
  }
}
`, name, testLocation())
	step2 := fmt.Sprintf(`
resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q
  labels      = { lb = "%[1]s" }
}

resource "odinex_load_balancer" "lb" {
  name               = "%[1]s-2"
  load_balancer_type = "lb21"
  location           = %[2]q
  labels             = { env = "test" }

  algorithm {
    type = "least_connections"
  }

  service {
    protocol    = "tcp"
    listen_port = 443
  }

  service {
    protocol         = "http"
    listen_port      = 80
    destination_port = 8080
    http {
      sticky_sessions = true
    }
    health_check {
      protocol = "http"
      port     = 8080
      http {
        path = "/healthz"
      }
    }
  }

  target {
    type           = "label_selector"
    label_selector = "lb=%[1]s"
  }

  target {
    type = "ip"
    ip   = "1.1.1.1"
  }
}
`, name, testLocation())
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: step1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "status", "available"),
					resource.TestMatchResourceAttr("odinex_load_balancer.lb", "ipv4", regexp.MustCompile(`\.`)),
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "service.#", "1"),
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "target.#", "1"),
				),
			},
			{
				Config: step2,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "load_balancer_type", "lb21"),
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "algorithm.type", "least_connections"),
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "service.#", "2"),
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "service.1.destination_port", "8080"),
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "service.1.health_check.http.path", "/healthz"),
					resource.TestCheckResourceAttr("odinex_load_balancer.lb", "target.#", "2"),
				),
			},
			{
				ResourceName:            "odinex_load_balancer.lb",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"service", "timeouts"},
			},
		},
	})
}

func TestAccSnapshotAndImageDataSource(t *testing.T) {
	name := accName()
	cfg := fmt.Sprintf(`
resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q
}

resource "odinex_snapshot" "snap" {
  server_id   = odinex_server.s.id
  description = "%[1]s"
  labels      = { snap = "%[1]s" }
}
`, name, testLocation())
	cfgDS := cfg + fmt.Sprintf(`
data "odinex_image" "latest" {
  with_type     = "snapshot"
  with_selector = "snap=%[1]s"
  most_recent   = true
  depends_on    = [odinex_snapshot.snap]
}
`, name)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("odinex_snapshot.snap", "server_id", "odinex_server.s", "id"),
					resource.TestCheckResourceAttr("odinex_snapshot.snap", "labels.snap", name),
				),
			},
			{ResourceName: "odinex_snapshot.snap", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				Config: cfgDS,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.odinex_image.latest", "id", "odinex_snapshot.snap", "id"),
					resource.TestCheckResourceAttr("data.odinex_image.latest", "type", "snapshot"),
				),
			},
		},
	})
}

func TestAccDataSources(t *testing.T) {
	name := accName()
	cfg := fmt.Sprintf(`
resource "odinex_ssh_key" "k" {
  name       = "%[1]s"
  public_key = %[3]q
  labels     = { ds = "%[1]s" }
}

resource "odinex_network" "n" {
  name     = "%[1]s"
  ip_range = "10.40.0.0/16"
  location = %[2]q
  labels   = { ds = "%[1]s" }
}

resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q
  labels      = { ds = "%[1]s" }
}

data "odinex_location" "l"     { name = %[2]q }
data "odinex_locations" "all"  {}
data "odinex_server_type" "t1" { name = "t1" }
data "odinex_server_types" "all" {}
data "odinex_load_balancer_type" "lb11" { name = "lb11" }
data "odinex_load_balancer_types" "all" {}
data "odinex_pricing" "p" { location = %[2]q }
data "odinex_image" "ubuntu" { name = "ubuntu-24.04" }

data "odinex_ssh_key" "byname" { name = odinex_ssh_key.k.name }
data "odinex_ssh_keys" "sel" {
  with_selector = "ds=%[1]s"
  depends_on    = [odinex_ssh_key.k]
}
data "odinex_network" "byname" { name = odinex_network.n.name }
data "odinex_server" "byid" { id = odinex_server.s.id }
data "odinex_server" "bysel" {
  with_selector = "ds=%[1]s"
  depends_on    = [odinex_server.s]
}
data "odinex_servers" "sel" {
  with_selector = "ds=%[1]s"
  with_statuses = ["running"]
  depends_on    = [odinex_server.s]
}
`, name, testLocation(), testKey)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.odinex_location.l", "name", testLocation()),
					resource.TestMatchResourceAttr("data.odinex_locations.all", "locations.#", regexp.MustCompile(`^[1-9]`)),
					resource.TestCheckResourceAttr("data.odinex_server_type.t1", "cores", "1"),
					resource.TestMatchResourceAttr("data.odinex_server_type.t1", "prices.0.price_monthly_net", regexp.MustCompile(`^\d`)),
					resource.TestMatchResourceAttr("data.odinex_server_types.all", "names.#", regexp.MustCompile(`^[1-9]`)),
					resource.TestCheckResourceAttr("data.odinex_load_balancer_type.lb11", "name", "lb11"),
					resource.TestMatchResourceAttr("data.odinex_load_balancer_types.all", "ids.#", regexp.MustCompile(`^[1-9]`)),
					resource.TestMatchResourceAttr("data.odinex_pricing.p", "prices.#", regexp.MustCompile(`^[1-9]`)),
					resource.TestCheckResourceAttr("data.odinex_image.ubuntu", "type", "system"),
					resource.TestCheckResourceAttrPair("data.odinex_ssh_key.byname", "fingerprint", "odinex_ssh_key.k", "fingerprint"),
					resource.TestCheckResourceAttr("data.odinex_ssh_keys.sel", "ssh_keys.#", "1"),
					resource.TestCheckResourceAttr("data.odinex_network.byname", "ip_range", "10.40.0.0/16"),
					resource.TestCheckResourceAttrPair("data.odinex_server.byid", "ipv4_address", "odinex_server.s", "ipv4_address"),
					resource.TestCheckResourceAttrPair("data.odinex_server.bysel", "id", "odinex_server.s", "id"),
					resource.TestCheckResourceAttr("data.odinex_servers.sel", "servers.#", "1"),
				),
			},
		},
	})
}

// testCheckServerFirewalls checks via the API which firewalls are applied
// directly to the server (independent of what is in state).
func testCheckServerFirewalls(serverAddr string, fwAddrs ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, err := client.New(client.Config{Endpoint: os.Getenv("ODINEX_ENDPOINT"), Token: os.Getenv("ODINEX_TOKEN")})
		if err != nil {
			return err
		}
		sid, err := client.ParseID(s.RootModule().Resources[serverAddr].Primary.ID)
		if err != nil {
			return err
		}
		got, err := serverFirewallIDs(context.Background(), c, sid)
		if err != nil {
			return err
		}
		want := []int64{}
		for _, a := range fwAddrs {
			id, err := client.ParseID(s.RootModule().Resources[a].Primary.ID)
			if err != nil {
				return err
			}
			want = append(want, id)
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("firewalls applied to the server: got %v, want %v", got, want)
		}
		return nil
	}
}

// TestAccServerFirewallIDs covers firewall_ids: set at creation (applied
// before the first boot), changed in place, and removed.
func TestAccServerFirewallIDs(t *testing.T) {
	name := accName()
	cfg := func(ids string) string {
		attr := ""
		if ids != "" {
			attr = "firewall_ids = " + ids
		}
		return fmt.Sprintf(`
resource "odinex_firewall" "a" {
  name = "%[1]s-a"
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = ["0.0.0.0/0"]
  }
}

resource "odinex_firewall" "b" {
  name = "%[1]s-b"
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q
  %[3]s
}
`, name, testLocation(), attr)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("[odinex_firewall.a.id]"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_server.s", "firewall_ids.#", "1"),
					resource.TestCheckTypeSetElemAttrPair("odinex_server.s", "firewall_ids.*", "odinex_firewall.a", "id"),
					testCheckServerFirewalls("odinex_server.s", "odinex_firewall.a"),
					// The firewall resource has no apply_to blocks, so it does not track (or fight) the target.
					resource.TestCheckNoResourceAttr("odinex_firewall.a", "apply_to.#"),
				),
			},
			{
				ResourceName:            "odinex_server.s",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"user_data", "ssh_keys", "keep_disk", "shutdown_before_deletion", "network", "public_net", "firewall_ids", "timeouts"},
			},
			{
				Config: cfg("[odinex_firewall.a.id, odinex_firewall.b.id]"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_server.s", "firewall_ids.#", "2"),
					testCheckServerFirewalls("odinex_server.s", "odinex_firewall.a", "odinex_firewall.b"),
				),
			},
			{
				Config: cfg("[odinex_firewall.b.id]"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_server.s", "firewall_ids.#", "1"),
					testCheckServerFirewalls("odinex_server.s", "odinex_firewall.b"),
				),
			},
			{
				// Removing the argument removes the firewalls it applied.
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("odinex_server.s", "firewall_ids.#"),
					testCheckServerFirewalls("odinex_server.s"),
				),
			},
		},
	})
}

// TestAccVPC covers a NAT gateway, a server without public IPs, an Elastic
// IP (assigned through odinex_elastic_ip_assignment, then removed) and a load
// balancer reaching the server over the network.
func TestAccVPC(t *testing.T) {
	name := accName()
	cfg := func(gwName string, assign bool) string {
		att := ""
		if assign {
			att = `
resource "odinex_elastic_ip_assignment" "ea" {
  elastic_ip_id = odinex_elastic_ip.e.id
  server_id     = odinex_server.s.id
  depends_on    = [odinex_nat_gateway.gw]
}
`
		}
		return fmt.Sprintf(`
resource "odinex_network" "n" {
  name     = "%[1]s"
  ip_range = "10.40.0.0/16"
  location = %[2]q
}

resource "odinex_nat_gateway" "gw" {
  name       = %[3]q
  network_id = odinex_network.n.id
  labels     = { env = "acc" }
}

resource "odinex_server" "s" {
  name        = "%[1]s"
  server_type = "t1"
  image       = "ubuntu-24.04"
  location    = %[2]q

  public_net {
    ipv4_enabled = false
    ipv6_enabled = false
  }

  network {
    network_id = odinex_network.n.id
  }

  depends_on = [odinex_nat_gateway.gw]
}

resource "odinex_elastic_ip" "e" {
  name     = "%[1]s"
  location = %[2]q
}

resource "odinex_load_balancer" "lb" {
  name               = "%[1]s"
  load_balancer_type = "lb11"
  location           = %[2]q
  network_id         = odinex_network.n.id

  service {
    protocol    = "tcp"
    listen_port = 22
  }

  target {
    type      = "server"
    server_id = odinex_server.s.id
  }
}
%[4]s`, name, testLocation(), gwName, att)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(name, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_network.n", "gateway", "10.40.0.1"),
					resource.TestCheckResourceAttr("odinex_nat_gateway.gw", "private_ip", "10.40.0.1"),
					resource.TestCheckResourceAttr("odinex_nat_gateway.gw", "location", testLocation()),
					resource.TestCheckResourceAttr("odinex_nat_gateway.gw", "status", "available"),
					resource.TestMatchResourceAttr("odinex_nat_gateway.gw", "public_ip", regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)),
					resource.TestCheckResourceAttrPair("odinex_server.s", "primary_network_id", "odinex_network.n", "id"),
					resource.TestCheckResourceAttr("odinex_server.s", "ipv4_address", ""),
					resource.TestCheckResourceAttr("odinex_server.s", "ipv6_address", ""),
					resource.TestCheckResourceAttr("odinex_server.s", "public_net.ipv4_enabled", "false"),
					resource.TestMatchResourceAttr("odinex_server.s", "network.0.ip", regexp.MustCompile(`^10\.40\.`)),
					resource.TestMatchResourceAttr("odinex_elastic_ip.e", "ip_address", regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)),
					resource.TestCheckResourceAttrPair("odinex_elastic_ip_assignment.ea", "id", "odinex_elastic_ip.e", "id"),
					resource.TestCheckResourceAttrPair("odinex_load_balancer.lb", "network_id", "odinex_network.n", "id"),
				),
			},
			{ResourceName: "odinex_nat_gateway.gw", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "odinex_nat_gateway.gw", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}, ImportStateId: name},
			{ResourceName: "odinex_elastic_ip_assignment.ea", ImportState: true, ImportStateVerify: true},
			{ResourceName: "odinex_elastic_ip.e", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "server_id"}},   // assigned by odinex_elastic_ip_assignment after creation
			{ResourceName: "odinex_network.n", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "nat_gateway_id"}}, // set when the NAT gateway is created
			{ResourceName: "odinex_load_balancer.lb", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"service", "timeouts"}},
			{
				// nat_gateway_id is Computed, so it is updated by a refresh.
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("odinex_network.n", "nat_gateway_id", "odinex_nat_gateway.gw", "id"),
					resource.TestCheckResourceAttrPair("odinex_elastic_ip.e", "server_id", "odinex_server.s", "id"),
				),
			},
			{
				// Renaming keeps the NAT gateway; removing the assignment unassigns the IP.
				Config: cfg(name+"-renamed", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odinex_nat_gateway.gw", "name", name+"-renamed"),
				),
			},
			{
				RefreshState: true,
				Check:        resource.TestCheckNoResourceAttr("odinex_elastic_ip.e", "server_id"),
			},
		},
	})
}
