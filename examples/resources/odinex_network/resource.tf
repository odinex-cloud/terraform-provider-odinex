resource "odinex_network" "private" {
  name     = "private"
  ip_range = "10.0.0.0/16"
  location = "fra1"
}
