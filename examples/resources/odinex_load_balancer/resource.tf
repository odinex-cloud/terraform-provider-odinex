resource "odinex_load_balancer" "web" {
  name               = "web"
  load_balancer_type = "lb11"
  location           = "fra1"

  # Reach server targets on their private address (needed for servers
  # without public IPs).
  network_id = odinex_network.private.id

  algorithm {
    type = "least_connections"
  }

  service {
    protocol    = "http"
    listen_port = 80
    health_check {
      protocol = "http"
      port     = 80
      http {
        path = "/healthz"
      }
    }
  }

  target {
    type           = "label_selector"
    label_selector = "role=web"
  }
}
