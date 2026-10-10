data "odinex_servers" "web" {
  with_selector = "role=web"
  with_statuses = ["running"]
}
