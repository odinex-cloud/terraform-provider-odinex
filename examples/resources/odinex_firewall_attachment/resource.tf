resource "odinex_firewall_attachment" "web" {
  firewall_id     = odinex_firewall.web.id
  server_ids      = [odinex_server.web.id]
  label_selectors = ["role=web"]
}
