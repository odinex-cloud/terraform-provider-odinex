resource "odinex_volume_attachment" "data" {
  volume_id = odinex_volume.data.id
  server_id = odinex_server.web.id
}
