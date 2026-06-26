output "public_ip" {
  description = "Public IPv4 of the benchmark instance."
  value       = aws_instance.bench.public_ip
}

output "public_dns" {
  description = "Public DNS of the benchmark instance."
  value       = aws_instance.bench.public_dns
}

output "ssh_user" {
  description = "SSH login user for Amazon Linux 2023."
  value       = "ec2-user"
}

output "ssh_private_key_path" {
  description = "Absolute path to the generated private key."
  value       = abspath(local_sensitive_file.private_key.filename)
}

output "instance_id" {
  value = aws_instance.bench.id
}

output "ami_id" {
  description = "Resolved AMI id used (record for reproducibility)."
  # SSM-resolved public AMI alias is not secret; unwrap so it can be recorded.
  value = nonsensitive(local.ami_id)
}
