output "public_ip" {
  description = "Public IP of the k3s node"
  value       = aws_instance.k3s.public_ip
}

output "ssh" {
  description = "Log in to the node"
  value       = "ssh -i ~/.ssh/launchpad ubuntu@${aws_instance.k3s.public_ip}"
}

output "bootstrap_log" {
  description = "Follow the bootstrap while it runs (takes about 5 minutes)"
  value       = "ssh -i ~/.ssh/launchpad ubuntu@${aws_instance.k3s.public_ip} tail -f /var/log/launchpad-bootstrap.log"
}

output "kubeconfig" {
  description = "Fetch a kubeconfig for this cluster once the bootstrap has finished"
  value       = "ssh -i ~/.ssh/launchpad ubuntu@${aws_instance.k3s.public_ip} cat /etc/rancher/k3s/k3s.yaml | sed 's/127.0.0.1/${aws_instance.k3s.public_ip}/' > ~/.kube/launchpad-aws"
}
