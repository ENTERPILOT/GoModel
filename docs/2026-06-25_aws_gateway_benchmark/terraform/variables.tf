variable "region" {
  description = "AWS region to provision the benchmark instance in."
  type        = string
  default     = "us-east-1"
}

variable "instance_type" {
  description = <<-EOT
    EC2 instance type. Default c7i.large (2 vCPU, 4 GiB, non-burstable) gives a
    stable tail with no CPU-credit drift — the right reference for latency/p99.
    It is NOT free-tier eligible (~$0.09/hr on-demand in us-east-1). For a
    free-tier run set instance_type=t2.micro (1 vCPU, burstable) explicitly;
    treat its absolute latencies as indicative only.
  EOT
  type        = string
  default     = "c7i.large"
}

variable "ssh_ingress_cidr" {
  description = "CIDR allowed to SSH in. Must be set explicitly to <your-ip>/32 (run.sh passes it automatically). No default, so the security group is never left open to the world."
  type        = string
  default     = ""

  validation {
    condition     = can(cidrhost(var.ssh_ingress_cidr, 0))
    error_message = "ssh_ingress_cidr must be a valid CIDR you pass explicitly (e.g. 203.0.113.4/32); it has no default to avoid a world-open (0.0.0.0/0) SSH rule."
  }
}

variable "ami_id" {
  description = "Override the AMI. Empty = latest Amazon Linux 2023 x86_64 via SSM (reproducible by policy, not by digest)."
  type        = string
  default     = ""
}

variable "root_volume_gb" {
  description = "Root EBS volume size (GiB). Free tier allows up to 30 GiB."
  type        = number
  default     = 20
}

variable "compose_plugin_version" {
  description = "Pinned Docker Compose v2 plugin version installed via user-data."
  type        = string
  default     = "v2.29.7"
}

variable "compose_plugin_sha256" {
  description = "Expected SHA-256 of the docker-compose-linux-x86_64 binary for compose_plugin_version (from the release's published .sha256). Update together with compose_plugin_version."
  type        = string
  default     = "383ce6698cd5d5bbf958d2c8489ed75094e34a77d340404d9f32c4ae9e12baf0"
}

variable "tags" {
  description = "Tags applied to all resources."
  type        = map(string)
  default = {
    Project = "gomodel-gateway-benchmark"
    Owner   = "benchmark"
  }
}
