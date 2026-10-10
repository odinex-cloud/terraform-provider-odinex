resource "odinex_server_network" "web" {
  server_id  = odinex_server.web.id
  network_id = odinex_network.private.id
  ip         = "10.0.1.5"
}
