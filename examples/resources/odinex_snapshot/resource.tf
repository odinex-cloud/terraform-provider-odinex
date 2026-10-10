resource "odinex_snapshot" "golden" {
  server_id   = odinex_server.web.id
  description = "golden image"
  labels      = { image = "golden" }
}
