# A complete Odinex Cloud setup: private network, firewall, two web servers
# with labels, a volume, a floating IP, a load balancer targeting the servers
# by label, and reverse DNS entries.
#
#   export ODINEX_TOKEN=odx_...
#   terraform init && terraform apply

terraform {
  required_providers {
    odinex = {
      source = "odinex-cloud/odinex"
    }
  }
}

provider "odinex" {
  # token    = var.odinex_token    # or ODINEX_TOKEN
  # endpoint = "https://api.odinex.cloud/v1"  # or ODINEX_ENDPOINT
}

variable "location" {
  type    = string
  default = "fra1"
}

variable "server_type" {
  type    = string
  default = "s2"
}

variable "environment" {
  type    = string
  default = "demo"
}

variable "domain" {
  description = "Domain for reverse DNS entries (must resolve to the IPs in production)."
  type        = string
  default     = "example.com"
}

data "odinex_image" "ubuntu" {
  name = "ubuntu-24.04"
}

data "odinex_server_type" "web" {
  name = var.server_type
}

resource "odinex_ssh_key" "admin" {
  name       = "admin"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGNA5kG6x3+5Vb7mE5uYbqgV0R2Fx7n6Qn5X1KJ8r3cT admin@example"
  labels     = { env = var.environment }
}

resource "odinex_network" "private" {
  name     = "private"
  ip_range = "10.0.0.0/16"
  location = var.location
  labels   = { env = var.environment }
}

resource "odinex_firewall" "web" {
  name   = "web"
  labels = { env = var.environment }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "icmp"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  apply_to {
    label_selector = "role=web"
  }
}

# Applied directly to the servers with firewall_ids (active before the first
# boot). This firewall has no apply_to blocks, so it does not track the servers.
resource "odinex_firewall" "ssh" {
  name   = "ssh"
  labels = { env = var.environment }

  rule {
    direction   = "in"
    protocol    = "tcp"
    port        = "22"
    source_ips  = ["0.0.0.0/0", "::/0"]
    description = "SSH"
  }
}

resource "odinex_server" "web" {
  count        = 2
  name         = "web-${count.index + 1}"
  server_type  = data.odinex_server_type.web.name
  image        = data.odinex_image.ubuntu.name
  location     = var.location
  ssh_keys     = [odinex_ssh_key.admin.id]
  firewall_ids = [odinex_firewall.ssh.id]
  keep_disk    = true

  labels = {
    env  = var.environment
    role = "web"
  }

  network {
    network_id = odinex_network.private.id
    ip         = "10.0.1.${count.index + 10}"
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }
}

resource "odinex_volume" "data" {
  name      = "web-1-data"
  size      = 10
  server_id = odinex_server.web[0].id
  labels    = { env = var.environment }
}

resource "odinex_floating_ip" "web" {
  type          = "ipv4"
  name          = "web"
  home_location = var.location
  labels        = { env = var.environment }
}

resource "odinex_floating_ip_assignment" "web" {
  floating_ip_id = odinex_floating_ip.web.id
  server_id      = odinex_server.web[0].id
}

resource "odinex_load_balancer" "web" {
  name               = "web"
  load_balancer_type = "lb11"
  location           = var.location
  labels             = { env = var.environment }

  algorithm {
    type = "round_robin"
  }

  service {
    protocol         = "http"
    listen_port      = 80
    destination_port = 80

    health_check {
      protocol = "http"
      port     = 80
      interval = 15
      timeout  = 10
      retries  = 3

      http {
        path         = "/"
        status_codes = ["2??", "3??"]
      }
    }
  }

  target {
    type           = "label_selector"
    label_selector = "role=web"
  }
}

resource "odinex_rdns" "web" {
  count      = 2
  server_id  = odinex_server.web[count.index].id
  ip_address = odinex_server.web[count.index].ipv4_address
  dns_ptr    = "web-${count.index + 1}.${var.domain}"
}

resource "odinex_rdns" "floating" {
  floating_ip_id = odinex_floating_ip.web.id
  ip_address     = odinex_floating_ip.web.ip_address
  dns_ptr        = "www.${var.domain}"
}

output "server_ipv4" {
  value = odinex_server.web[*].ipv4_address
}

output "floating_ip" {
  value = odinex_floating_ip.web.ip_address
}

output "load_balancer_ipv4" {
  value = odinex_load_balancer.web.ipv4
}

output "server_type_monthly_price" {
  value = [for p in data.odinex_server_type.web.prices : p.price_monthly_gross if p.location == var.location][0]
}
