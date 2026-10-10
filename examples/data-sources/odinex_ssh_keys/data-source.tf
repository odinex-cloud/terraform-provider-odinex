data "odinex_ssh_keys" "team" {
  with_selector = "team=ops"
}
