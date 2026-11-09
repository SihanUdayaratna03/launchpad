variable "region" {
  description = "AWS region"
  type        = string
  default     = "ap-southeast-1"
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
  description = "t3.small has 2 GiB RAM, enough for k3s + Argo CD. Use t3.medium once Prometheus and Grafana are added."
  type        = string
  default     = "t3.medium"
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
