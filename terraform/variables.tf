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

variable "dns_suffix" {
  description = "Wildcard DNS service that turns <anything>.<ip-with-dashes>.<suffix> into the node's IP. It names the hosts when duckdns_name is empty. nip.io and sslip.io are not on the Public Suffix List, so Let's Encrypt counts every certificate under them against one limit of 50 a week for the whole world: fine for the staging CA, not for a real certificate. Use duckdns_name for that."
  type        = string
  default     = "nip.io"

  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$", var.dns_suffix))
    error_message = "dns_suffix must be a lowercase domain name such as nip.io."
  }
}

variable "duckdns_name" {
  description = "Your DuckDNS subdomain, e.g. launchpad-sihan for launchpad-sihan.duckdns.org. Previews are then pr-N.<name>.duckdns.org, whatever the node's IP is. duckdns.org is on the Public Suffix List, so your subdomain gets Let's Encrypt's 50 certificates a week to itself. Terraform points it at the Elastic IP on every apply. argocd bootstrap only."
  type        = string
  default     = ""

  validation {
    condition     = var.duckdns_name == "" || can(regex("^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$", var.duckdns_name))
    error_message = "duckdns_name is the part before .duckdns.org: lowercase letters, digits and hyphens."
  }
}

variable "duckdns_token" {
  description = "Your DuckDNS token. Pass it as TF_VAR_duckdns_token rather than writing it into a file. It is only used by a local curl, so it never reaches the instance or its user data."
  type        = string
  default     = ""
  sensitive   = true
}

variable "acme_email" {
  description = "Contact address for Let's Encrypt. Setting it turns on TLS for the argocd bootstrap: Traefik obtains a certificate for every preview and for the webhook host. Empty leaves everything on plain http."
  type        = string
  default     = ""

  validation {
    condition     = var.acme_email == "" || can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.acme_email))
    error_message = "acme_email must be an email address, or empty."
  }
}

variable "acme_staging" {
  description = "Use Let's Encrypt's staging CA. Its certificates are not trusted by browsers, but it has its own, far higher limits, so a first run does not spend the real ones. Set it to false once the staging run works."
  type        = bool
  default     = true
}
