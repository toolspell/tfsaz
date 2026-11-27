# Builds the Ubuntu base VM from Canonical's server ISO. Run it through
# `go run ./tools/live base build ubuntu`, which downloads and checks the ISO,
# creates the seed ISO and the build key, and sets TART_HOME.
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

variable "iso" {
  type        = string
  description = "Absolute path to the Ubuntu live server ISO."
}

variable "seed_iso" {
  type        = string
  description = "Absolute path to the cidata ISO with the autoinstall config."
}

variable "ssh_private_key_file" {
  type        = string
  description = "Key whose public half is in the autoinstall config."
}

source "tart-cli" "ubuntu" {
  from_iso     = [var.seed_iso, var.iso]
  vm_name      = var.vm_name
  cpu_count    = 4
  memory_gb    = 4
  disk_size_gb = 40
  headless     = true
  # GRUB boots the installer after its timeout. The installer asks before it
  # uses an autoinstall config that is not on the kernel command line.
  boot_command = [
    "<wait 'Continue with autoinstall'>yes<enter>",
  ]
  ssh_username         = "tester"
  ssh_private_key_file = var.ssh_private_key_file
  ssh_timeout          = "25m"
}

build {
  sources = ["source.tart-cli.ubuntu"]

  provisioner "shell" {
    execute_command = "sudo sh -eu '{{ .Path }}'"
    script          = "${path.root}/prepare-base.sh"
  }
}
