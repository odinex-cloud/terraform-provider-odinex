data "odinex_image" "ubuntu" {
  name = "ubuntu-24.04"
}

# Newest snapshot with a label.
data "odinex_image" "golden" {
  with_type     = "snapshot"
  with_selector = "image=golden"
  most_recent   = true
}
