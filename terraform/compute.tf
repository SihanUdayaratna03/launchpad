data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
}

resource "aws_key_pair" "launchpad" {
  key_name   = "launchpad"
  public_key = file(pathexpand(var.public_key_path))
}

locals {
  # TLS is only wired into the argocd bootstrap: the openchoreo one has no
  # Traefik, and its gateway terminates TLS in its own way.
  tls_enabled = var.acme_email != "" && var.bootstrap == "argocd"

  # The hostnames that wildcard DNS derives from the address, and the DuckDNS
  # name that replaces them when one is given. OpenChoreo keeps the first: its
  # hosts are baked into the install by hand and it serves plain http.
  ip_domain    = "${replace(aws_eip.k3s.public_ip, ".", "-")}.${var.dns_suffix}"
  duckdns_fqdn = "${var.duckdns_name}.duckdns.org"
  use_duckdns  = var.duckdns_name != "" && var.bootstrap == "argocd"
  base_domain  = local.use_duckdns ? local.duckdns_fqdn : local.ip_domain
}

# The address is allocated first and handed to the bootstrap script, which
# builds every hostname from it. Reading it from the instance metadata instead
# would race the association: cloud-init can run before the Elastic IP is
# attached and see the throwaway address the instance launched with.
resource "aws_eip" "k3s" {
  domain = "vpc"
  tags   = { Name = "launchpad-k3s-${var.bootstrap}" }

  depends_on = [aws_internet_gateway.main]
}

# DuckDNS holds one A record per subdomain, and answers for every name below
# it, so this single update is what makes pr-N.<name>.duckdns.org and
# webhook.<name>.duckdns.org resolve. It runs as soon as the address exists,
# well before the instance has booted and Traefik asks for a certificate.
#
# The token goes in through the environment and into curl by stdin, so it is
# not on a command line, in the plan output or in the state.
resource "terraform_data" "duckdns" {
  count = local.use_duckdns ? 1 : 0

  triggers_replace = [aws_eip.k3s.public_ip, var.duckdns_name]

  lifecycle {
    precondition {
      condition     = var.duckdns_token != ""
      error_message = "duckdns_name is set but duckdns_token is empty. Export TF_VAR_duckdns_token."
    }
  }

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      set -euo pipefail
      printf 'url = "https://www.duckdns.org/update?domains=%s&token=%s&ip=%s"\n' \
        "$DUCKDNS_NAME" "$DUCKDNS_TOKEN" "$NODE_IP" \
        | curl -sS --fail --retry 3 -K - | grep -qx OK
    EOT
    environment = {
      DUCKDNS_NAME  = var.duckdns_name
      DUCKDNS_TOKEN = var.duckdns_token
      NODE_IP       = aws_eip.k3s.public_ip
    }
  }
}

resource "aws_eip_association" "k3s" {
  instance_id   = aws_instance.k3s.id
  allocation_id = aws_eip.k3s.id
}

resource "aws_instance" "k3s" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.node.id]
  key_name               = aws_key_pair.launchpad.key_name
  ebs_optimized          = true

  user_data = var.bootstrap == "openchoreo" ? templatefile("${path.module}/user-data-openchoreo.sh.tftpl", {
    k3s_version = var.k3s_version
    public_ip   = aws_eip.k3s.public_ip
    dns_suffix  = var.dns_suffix
    }) : templatefile("${path.module}/user-data.sh.tftpl", {
    config_repo    = var.config_repo
    argocd_version = var.argocd_version
    public_ip      = aws_eip.k3s.public_ip
    base_domain    = local.base_domain
    acme_email     = local.tls_enabled ? var.acme_email : ""
    acme_ca        = var.acme_staging ? "https://acme-staging-v02.api.letsencrypt.org/directory" : "https://acme-v02.api.letsencrypt.org/directory"
  })
  user_data_replace_on_change = true

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = var.root_volume_size
    encrypted   = true
  }

  tags = { Name = "launchpad-k3s-${var.bootstrap}" }

  lifecycle {
    precondition {
      condition     = !(local.tls_enabled && !var.acme_staging && !local.use_duckdns)
      error_message = "A real Let's Encrypt certificate under ${var.dns_suffix} would count against a limit shared with everyone using it. Set duckdns_name, or keep acme_staging = true."
    }
    precondition {
      condition     = var.duckdns_name == "" || var.bootstrap == "argocd"
      error_message = "duckdns_name only applies to the argocd bootstrap."
    }
  }
}
