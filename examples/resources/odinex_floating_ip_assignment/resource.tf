resource "odinex_floating_ip_assignment" "web" {
  floating_ip_id = odinex_floating_ip.web.id
  server_id      = odinex_server.web.id
}
