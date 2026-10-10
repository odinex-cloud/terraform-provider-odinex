resource "odinex_nat_gateway" "private" {
  name       = "private"
  network_id = odinex_network.private.id
}
