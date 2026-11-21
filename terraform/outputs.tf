output "public_ip" {
  description = "Elastic IP of the k3s node. It is released on destroy, so the next apply gets a different one."
  value       = aws_eip.k3s.public_ip
}

output "ssh" {
  description = "Log in to the node"
  value       = "ssh -i ~/.ssh/launchpad ubuntu@${aws_eip.k3s.public_ip}"
}

output "bootstrap_log" {
  description = "Follow the bootstrap while it runs (takes about 5 minutes)"
  value       = "ssh -i ~/.ssh/launchpad ubuntu@${aws_eip.k3s.public_ip} tail -f /var/log/launchpad-bootstrap.log"
}

output "openchoreo" {
  description = "Hostnames and the webhook URL for the openchoreo bootstrap"
  value = var.bootstrap != "openchoreo" ? null : {
    control_plane_domain = "openchoreo.${local.base_domain}"
    data_plane_domain    = "apps.openchoreo.${local.base_domain}"
    webhook_url          = "http://${aws_eip.k3s.public_ip}:${var.webhook_node_port}/webhook"
  }
}

output "kubeconfig" {
  description = "Fetch a kubeconfig for this cluster once the bootstrap has finished"
  value       = "ssh -i ~/.ssh/launchpad ubuntu@${aws_eip.k3s.public_ip} cat /etc/rancher/k3s/k3s.yaml | sed 's/127.0.0.1/${aws_eip.k3s.public_ip}/' > ~/.kube/launchpad-aws"
}

output "argocd" {
  description = "Preview domain and webhook URL for the argocd bootstrap"
  value = var.bootstrap != "argocd" ? null : {
    preview_domain = local.base_domain
    tls            = local.tls_enabled
    staging_ca     = local.tls_enabled && var.acme_staging
    webhook_url    = local.tls_enabled ? "https://webhook.${local.base_domain}/webhook" : "http://${aws_eip.k3s.public_ip}/webhook"
  }
}
