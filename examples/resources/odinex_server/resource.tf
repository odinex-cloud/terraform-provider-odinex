resource "odinex_server" "web" {
  name         = "web-1"
  server_type  = "s2"
  image        = "ubuntu-24.04"
  location     = "fra1"
  ssh_keys     = [odinex_ssh_key.admin.id]
  firewall_ids = [odinex_firewall.ssh.id]
  backups      = true
  labels       = { role = "web" }

  network {
    network_id = odinex_network.private.id
    ip         = "10.0.1.5"
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }
}

# A server without public IPs: it reaches the internet through the network's
# NAT gateway and is reached through an Elastic IP or a load balancer.
resource "odinex_server" "app" {
  name        = "app-1"
  server_type = "s2"
  image       = "ubuntu-24.04"
  location    = "fra1"
  ssh_keys    = [odinex_ssh_key.admin.id]

  network {
    network_id = odinex_network.private.id
  }

  public_net {
    ipv4_enabled = false
    ipv6_enabled = false
  }

  depends_on = [odinex_nat_gateway.private]
}
