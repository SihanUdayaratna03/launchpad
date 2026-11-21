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
  base_domain = "${replace(aws_eip.k3s.public_ip, ".", "-")}.${var.dns_suffix}"
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
    dns_suffix     = var.dns_suffix
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
}
