terraform {
  required_providers {
    odinex = {
      source = "odinex-cloud/odinex"
    }
  }
}

# Configure with a project API token. Prefer the ODINEX_TOKEN environment variable.
provider "odinex" {
  token = var.odinex_token
}

variable "odinex_token" {
  type      = string
  sensitive = true
}
