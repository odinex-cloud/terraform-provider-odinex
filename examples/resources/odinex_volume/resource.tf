resource "odinex_volume" "data" {
  name      = "data"
  size      = 50
  server_id = odinex_server.web.id
}
