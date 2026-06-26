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
  description = "CIDR allowed to SSH in. Set to <your-ip>/32. Defaults to fully open if left empty (NOT recommended)."
  type        = string
  default     = "0.0.0.0/0"
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

variable "tags" {
  description = "Tags applied to all resources."
  type        = map(string)
  default = {
    Project = "gomodel-gateway-benchmark"
    Owner   = "benchmark"
  }
}
