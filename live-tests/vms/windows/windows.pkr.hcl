# Builds the Windows base VM from Microsoft's Windows 11 ARM64 ISO with QEMU.
# Run it through `go run ./tools/live base build windows`, which checks the
# ISOs, renders the seed ISO and the build key, and passes the paths below.
packer {
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "1.1.8"
    }
  }
}

variable "vm_name" {
  type        = string
  description = "Unused: the qemu builder names the disk; output_dir names the VM."
}

variable "iso" {
  type        = string
  description = "Absolute path to Microsoft's Windows 11 ARM64 ISO."
}

variable "virtio_iso" {
  type        = string
  description = "Absolute path to the virtio-win ISO with the network driver."
}

variable "seed_iso" {
  type        = string
  description = "Absolute path to the ISO with Autounattend.xml and setup.ps1."
}

variable "efi_code" {
  type = string
}

variable "efi_vars" {
  type        = string
  description = "Empty UEFI variable store; Packer copies it to the VM."
}

variable "output_dir" {
  type        = string
  description = "The VM's directory in the qemu provider's home."
}

variable "qmp_socket" {
  type        = string
  description = "QMP socket, for example for screendump when a build hangs."
}

variable "ssh_private_key_file" {
  type        = string
  description = "Key whose public half is in setup.ps1."
}

source "qemu" "windows" {
  iso_url          = var.iso
  iso_checksum     = "none" # base build checked it against iso.json
  output_directory = var.output_dir
  vm_name          = "disk.qcow2"
  format           = "qcow2"
  disk_size        = "64G"
  qemu_binary      = "qemu-system-aarch64"
  accelerator      = "hvf"
  machine_type     = "virt,gic-version=max"
  cpu_model        = "host"
  cpus             = 4
  memory           = 8192
  net_device       = "virtio-net-pci"
  headless         = true

  efi_boot          = true
  efi_firmware_code = var.efi_code
  efi_firmware_vars = var.efi_vars

  # These replace all of Packer's drives and devices. The virt machine has
  # no IDE or SATA, so the ISOs are USB drives, and Windows has inbox
  # drivers for NVMe and USB. tools/live/qemu.go starts the clones with the
  # same hardware.
  qemuargs = [
    ["-drive", "if=pflash,format=raw,readonly=on,file=${var.efi_code}"],
    ["-drive", "if=pflash,format=raw,file={{ .OutputDir }}/efivars.fd"],
    ["-drive", "if=none,id=disk,format=qcow2,discard=unmap,file={{ .OutputDir }}/disk.qcow2"],
    ["-drive", "if=none,id=install,media=cdrom,readonly=on,file=${var.iso}"],
    ["-drive", "if=none,id=seed,media=cdrom,readonly=on,file=${var.seed_iso}"],
    ["-drive", "if=none,id=virtio,media=cdrom,readonly=on,file=${var.virtio_iso}"],
    ["-device", "nvme,drive=disk,serial=disk0"],
    ["-device", "qemu-xhci"],
    ["-device", "usb-storage,drive=install"],
    ["-device", "usb-storage,drive=seed"],
    ["-device", "usb-storage,drive=virtio"],
    ["-device", "usb-kbd"],
    ["-device", "usb-tablet"],
    ["-device", "ramfb"],
    ["-device", "virtio-net-pci,netdev=user.0"],
    ["-netdev", "user,id=user.0,hostfwd=tcp:127.0.0.1:{{ .SSHHostPort }}-:22"],
    ["-qmp", "unix:${var.qmp_socket},server=on,wait=off"],
    ["-display", "none"],
  ]

  # The ISO's boot loader waits a few seconds for a key before Setup starts.
  boot_wait    = "1s"
  boot_command = ["<enter><wait><enter><wait><enter><wait><enter><wait><enter><wait><enter><wait><enter><wait><enter><wait><enter><wait><enter>"]

  communicator         = "ssh"
  ssh_username         = "tester"
  ssh_private_key_file = var.ssh_private_key_file
  ssh_timeout          = "60m"
  # prepare-base.ps1 shuts the VM down.
  shutdown_command = "powershell -NoProfile -ExecutionPolicy Bypass -File C:\\Windows\\Temp\\prepare-base.ps1"
  shutdown_timeout = "15m"
}

build {
  sources = ["source.qemu.windows"]

  provisioner "powershell" {
    script = "${path.root}/provision.ps1"
  }

  provisioner "file" {
    source      = "${path.root}/prepare-base.ps1"
    destination = "C:/Windows/Temp/prepare-base.ps1"
  }
}
