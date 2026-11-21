variable "region" {
  description = "AWS region"
  type        = string
  default     = "ap-southeast-2"
}

variable "my_ip_cidr" {
  description = "Your public IP in CIDR form, e.g. 203.0.113.7/32. SSH and the Kubernetes API are open to this address only."
  type        = string

  validation {
    condition     = can(cidrhost(var.my_ip_cidr, 0)) && var.my_ip_cidr != "0.0.0.0/0"
    error_message = "Use your own IP as a /32, not 0.0.0.0/0."
  }
}

variable "public_key_path" {
  description = "Path to the SSH public key uploaded to AWS"
  type        = string
  default     = "~/.ssh/launchpad.pub"
}

variable "instance_type" {
  description = "t3.small has 2 GiB RAM, enough for k3s + Argo CD. Use t3.medium once Prometheus and Grafana are added. OpenChoreo needs 8 GiB, and m7i-flex.large is the largest free-tier-eligible type that has it."
  type        = string
  default     = "t3.medium"
}

variable "bootstrap" {
  description = "What the node comes up as: argocd (k3s + Argo CD + the GitOps root app) or openchoreo (k3s with traefik disabled and helm, ready for the OpenChoreo install by hand)."
  type        = string
  default     = "argocd"

  validation {
    condition     = contains(["argocd", "openchoreo"], var.bootstrap)
    error_message = "bootstrap must be argocd or openchoreo."
  }
}

variable "root_volume_size" {
  description = "Root disk in GiB. OpenChoreo pulls a lot of images, so give it 40."
  type        = number
  default     = 20
}

variable "k3s_version" {
  description = "k3s release to install, e.g. v1.34.1+k3s1. Empty installs the current stable. OpenChoreo needs Kubernetes 1.34 or later."
  type        = string
  default     = ""
}

variable "webhook_node_port" {
  description = "NodePort the Launchpad webhook is published on in the openchoreo bootstrap, where traefik is not there to do it."
  type        = number
  default     = 30080
}

variable "config_repo" {
  description = "GitHub owner/repo that holds the desired cluster state"
  type        = string
  default     = "SihanUdayaratna03/launchpad-config"
}

variable "argocd_version" {
  description = "Argo CD release tag to install. Pin it to the version you tested on kind."
  type        = string
  default     = "stable"
}
