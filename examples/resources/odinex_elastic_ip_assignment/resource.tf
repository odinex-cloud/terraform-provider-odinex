resource "odinex_elastic_ip_assignment" "app" {
  elastic_ip_id = odinex_elastic_ip.app.id
  server_id     = odinex_server.app.id
}
