# Builds the macOS base VM from Cirrus Labs' vanilla image, pinned by digest
# in image.json. Run it through `go run ./tools/live base build macos`,
# which creates the build key and sets TART_HOME.
packer {
  required_plugins {
    tart = {
      source  = "github.com/cirruslabs/tart"
      version = "1.21.0"
    }
  }
}

variable "vm_name" {
  type = string
}

variable "base_image" {
  type        = string
  description = "Cirrus Labs vanilla macOS image, pinned by digest."
}

variable "ssh_private_key_file" {
  type = string
}

variable "ssh_public_key" {
  type        = string
  description = "Public half of ssh_private_key_file; provision.sh makes it the only SSH login."
}

source "tart-cli" "macos" {
  vm_base_name = var.base_image
  vm_name      = var.vm_name
  cpu_count    = 4
  memory_gb    = 8
  headless     = true
  # The vanilla image's login. provision.sh turns off password login over
  # SSH; Packer then keeps working with the build key.
  ssh_username         = "admin"
  ssh_password         = "admin"
  ssh_private_key_file = var.ssh_private_key_file
  ssh_timeout          = "10m"
}

build {
  sources = ["source.tart-cli.macos"]

  provisioner "shell" {
    environment_vars = ["SSH_PUBLIC_KEY=${var.ssh_public_key}"]
    script           = "${path.root}/provision.sh"
  }

  provisioner "shell" {
    execute_command = "sudo sh -eu '{{ .Path }}'"
    script          = "${path.root}/prepare-base.sh"
  }
}
