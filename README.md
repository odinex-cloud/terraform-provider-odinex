# Terraform / OpenTofu provider for Odinex Cloud

`registry.terraform.io/odinex-cloud/odinex` — manage Odinex Cloud servers, networks,
volumes, firewalls, floating IPs, load balancers and more with Terraform or
OpenTofu. Built with the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework)
and modelled on [hetznercloud/terraform-provider-hcloud](https://github.com/hetznercloud/terraform-provider-hcloud):
resource names, arguments, import IDs and timeouts follow hcloud wherever the
Odinex API allows it.

```hcl
terraform {
  required_providers {
    odinex = { source = "odinex-cloud/odinex" }
  }
}

provider "odinex" {}   # token from ODINEX_TOKEN

resource "odinex_server" "web" {
  name        = "web-1"
  server_type = "s2"
  image       = "ubuntu-24.04"
  location    = "fra1"
  labels      = { role = "web" }
}
```

A complete example (network, firewall, two labelled servers, volume, floating
IP, load balancer targeting the label, reverse DNS) is in
[`examples/complete/main.tf`](examples/complete/main.tf). Per-resource docs are
in [`docs/`](docs/).

## Provider configuration

| Argument        | Environment variable   | Default                          |
|-----------------|------------------------|----------------------------------|
| `token`         | `ODINEX_TOKEN`         | — (required, sensitive)          |
| `endpoint`      | `ODINEX_ENDPOINT`      | `https://api.odinex.cloud/v1`    |
| `poll_interval` | `ODINEX_POLL_INTERVAL` | `1s` (Go duration)               |

The token is a project API token (`odx_…`, scope `read_write` for changes);
everything the provider manages lives in that project.

Long-running operations return an action; the provider polls
`GET /actions/{id}` every `poll_interval` and reports a failed action
(`status = error`) as an error diagnostic with the action's code and message.
Requests answered with `429` are retried after `Retry-After` (the API allows
3600 requests per hour and token), and `409 resource_busy` (another action is
running on the resource) is retried with a short back-off.

## Resources

| Resource | Notes | Import ID |
|---|---|---|
| `odinex_server` | `server_type` changes in place (power off → `change_type` → power on; `keep_disk = true` keeps the disk so you can downgrade later). Changing `image` rebuilds. `backups`, `labels`, `delete_protection`, `name`, `firewall_ids` (applied at creation, before the first boot; then via apply/remove actions) and `network` blocks change in place. `location`, `ssh_keys`, `user_data`, `placement_group_id`, `public_net` and `primary_network_id` force a new server. A server without public IPs disables both in `public_net` and needs a network (`primary_network_id`, or a single `network` block). Computed: `primary_network_id`, `ipv4_address`, `ipv6_address`, `primary_ipv4_id`, `primary_ipv6_id`, `status`, `backup_window`, `primary_disk_size`. Timeouts: create/update/delete. | ID or name |
| `odinex_ssh_key` | `public_key` forces a new key. | ID or name |
| `odinex_volume` | `size` grows online; shrinking recreates. `server_id` attaches/detaches. | ID or name |
| `odinex_volume_attachment` | | volume ID |
| `odinex_network` | `ip_range` and `location` force a new network. Computed: `gateway`, `nat_gateway_id`. | ID or name |
| `odinex_server_network` | Optional fixed `ip`. | `<server_id>-<network_id>` |
| `odinex_firewall` | `rule` and `apply_to` blocks, updated with `set_rules` / `apply_to_resources` / `remove_from_resources`. | ID or name |
| `odinex_firewall_attachment` | `server_ids`, `label_selectors`. | firewall ID |
| `odinex_floating_ip` | `type`, `home_location`, `server_id`, `name`, `description`. | ID or name |
| `odinex_floating_ip_assignment` | | floating IP ID |
| `odinex_nat_gateway` | One per network (`network_id` forces a new gateway); takes the network's `gateway` address. Computed: `public_ip`, `private_ip`. | ID or name |
| `odinex_elastic_ip` | Public IPv4 NATed to a server without public IPs (its network needs a NAT gateway). `location`, `server_id`, `name`. | ID or name |
| `odinex_elastic_ip_assignment` | | Elastic IP ID |
| `odinex_rdns` | PTR for a server's public IP, a primary IP or a floating IP (one of `server_id`, `primary_ip_id`, `floating_ip_id`). | `s-<server_id>-<ip>`, `p-<primary_ip_id>-<ip>`, `f-<floating_ip_id>-<ip>` |
| `odinex_load_balancer` | Inline `service`, `target` and `algorithm` blocks; `load_balancer_type` and `network_id` (attach/detach) change in place. | ID or name |
| `odinex_placement_group` | `spread`. | ID or name |
| `odinex_snapshot` | Snapshot of a server's disk (`server_id`). | image ID |

All resources with labels expose `labels`; all resources that support delete
protection expose `delete_protection`. `Read` removes a resource from state
when the API answers 404.

Like in hcloud, some relationships can be managed in two ways — use one of
them per object:

* `odinex_server.network` blocks **or** `odinex_server_network`
  (the server only tracks networks while at least one `network` block is configured),
* for a firewall on a server: `odinex_server.firewall_ids` **or**
  `odinex_firewall.apply_to` blocks **or** `odinex_firewall_attachment`
  (`firewall_ids` and `apply_to` are only tracked while set; `firewall_ids`
  lists firewalls applied directly to the server, not through label selectors),
* `odinex_volume.server_id` **or** `odinex_volume_attachment`,
* `odinex_floating_ip.server_id` **or** `odinex_floating_ip_assignment`,
* `odinex_elastic_ip.server_id` **or** `odinex_elastic_ip_assignment`.

`server_id` on volumes, floating IPs and Elastic IPs is optional *and* computed, so an
assignment made by the attachment resources shows up there after the next
refresh without causing a diff.

## Data sources

`odinex_location`, `odinex_locations`, `odinex_server_type`, `odinex_server_types`,
`odinex_image` (by `id`, by `name`, or by `with_type` + `with_selector`
with `most_recent`; `name` and `with_selector` are filtered by the API), `odinex_ssh_key`, `odinex_ssh_keys`, `odinex_server`,
`odinex_servers` (by `id`, `name`, `with_selector`, status), `odinex_network`,
`odinex_load_balancer_type`, `odinex_load_balancer_types`, `odinex_pricing`.

## Development

The provider is its own Go module (`odinex.cloud/terraform-provider-odinex`).

```sh
make generate   # regenerate internal/client from ../api/openapi.yaml (oapi-codegen; also `go generate ./...`)
make build      # bin/terraform-provider-odinex
make vet test   # go vet + unit tests
make docs       # regenerate docs/ with tfplugindocs (uses examples/)
```

### Using a local build

Point a CLI configuration file at the build directory with `dev_overrides`
(keep it out of your own `~/.terraformrc` if you like and select it with
`TF_CLI_CONFIG_FILE`):

```hcl
provider_installation {
  dev_overrides {
    "odinex-cloud/odinex" = "/path/to/terraform-provider-odinex/bin"
  }
  direct {}
}
```

```sh
export TF_CLI_CONFIG_FILE=$PWD/dev.tfrc
export ODINEX_ENDPOINT=http://localhost:18090/v1 ODINEX_TOKEN=odx_...
cd examples/complete && terraform plan    # no `terraform init` needed with dev_overrides
```

OpenTofu works the same way (`tofu plan`, `TF_CLI_CONFIG_FILE` is honoured too).

### Acceptance tests against the dev backend

1. Start the dev stack (see the repository README): `make dev-up migrate seed`,
   then `./bin/odinex api` and `ODINEX_WORKER_REGIONS=fra1 ./bin/odinex worker`
   (`ODINEX_OPENSTACK_FRA1_FAKE_DELAY=1s` keeps the fake driver quick).
2. Create a project token the way `scripts/smoke_test.py` does: sign up
   (`GET /v1/auth/csrf`, `POST /v1/auth/signup`), verify the email via
   Mailpit (`POST /v1/auth/verify-email`), credit the wallet
   (`./bin/odinex wallet-credit --org <id> --amount 20`) and
   `POST /v1/projects/{id}/tokens` with `{"name": "tf", "scope": "read_write"}`.
3. Run:

```sh
TF_ACC=1 ODINEX_ENDPOINT=http://localhost:18090/v1 ODINEX_TOKEN=odx_... make testacc
```

The acceptance tests (`internal/provider/acc_test.go`) create, update, import
and destroy every resource and read every data source. `ODINEX_TEST_LOCATION`
overrides the location (default `fra1`).
