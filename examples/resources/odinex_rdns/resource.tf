resource "odinex_rdns" "web" {
  server_id  = odinex_server.web.id
  ip_address = odinex_server.web.ipv4_address
  dns_ptr    = "web-1.example.com"
}

resource "odinex_rdns" "floating" {
  floating_ip_id = odinex_floating_ip.web.id
  ip_address     = odinex_floating_ip.web.ip_address
  dns_ptr        = "www.example.com"
}
