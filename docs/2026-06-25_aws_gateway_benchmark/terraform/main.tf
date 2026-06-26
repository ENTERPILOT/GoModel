terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.60"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
  }
}

provider "aws" {
  region = var.region
}

# ── AMI: latest Amazon Linux 2023 x86_64 (override via var.ami_id) ──
data "aws_ssm_parameter" "al2023" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
}

locals {
  ami_id = var.ami_id != "" ? var.ami_id : data.aws_ssm_parameter.al2023.value
  # credit_specification is only valid for burstable T-family instances; on a
  # fixed-performance type (c7i.large default) it must be omitted entirely.
  is_burstable = can(regex("^t[0-9]", var.instance_type))
}

# ── Default VPC / subnet (free-tier friendly, no NAT) ──────────────
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

# ── SSH keypair generated locally, written to disk for the runner ──
resource "tls_private_key" "bench" {
  algorithm = "ED25519"
}

resource "local_sensitive_file" "private_key" {
  content         = tls_private_key.bench.private_key_openssh
  filename        = "${path.module}/bench_key.pem"
  file_permission = "0600"
}

resource "aws_key_pair" "bench" {
  key_name_prefix = "gomodel-bench-"
  public_key      = tls_private_key.bench.public_key_openssh
  tags            = var.tags
}

# ── Security group: SSH only, from the operator's IP ───────────────
resource "aws_security_group" "bench" {
  name_prefix = "gomodel-bench-"
  description = "SSH access for the gateway benchmark instance"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.ssh_ingress_cidr]
  }

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = var.tags
}

# ── Instance bootstrap: install docker + compose plugin ────────────
locals {
  user_data = <<-EOF
    #!/bin/bash
    set -euxo pipefail

    # 2 GiB swap: a 1 GiB free-tier instance can't hold memory-heavy gateways
    # (LiteLLM idles near ~1 GiB). Swap lets every gateway run so the memory
    # comparison is complete; the reported RSS still exposes the difference.
    if [ ! -f /swapfile ]; then
      fallocate -l 2G /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=2048
      chmod 600 /swapfile
      mkswap /swapfile
      swapon /swapfile
      echo '/swapfile none swap sw 0 0' >> /etc/fstab
    fi

    dnf update -y
    dnf install -y docker git
    systemctl enable --now docker
    usermod -aG docker ec2-user

    # Docker Compose v2 plugin (pinned).
    mkdir -p /usr/libexec/docker/cli-plugins
    curl -fsSL -o /usr/libexec/docker/cli-plugins/docker-compose \
      "https://github.com/docker/compose/releases/download/${var.compose_plugin_version}/docker-compose-linux-x86_64"
    chmod +x /usr/libexec/docker/cli-plugins/docker-compose

    # Readiness marker the orchestrator polls for.
    touch /home/ec2-user/.bootstrap-done
  EOF
}

resource "aws_instance" "bench" {
  ami                         = local.ami_id
  instance_type               = var.instance_type
  key_name                    = aws_key_pair.bench.key_name
  vpc_security_group_ids      = [aws_security_group.bench.id]
  subnet_id                   = tolist(data.aws_subnets.default.ids)[0]
  associate_public_ip_address = true
  user_data                   = local.user_data

  # Only burstable (T-family) instances accept a credit specification. Standard
  # credits avoid surprise burst charges there; fixed-performance types (the
  # c7i.large default) omit this block entirely — and have no credit drift, which
  # is exactly why they make the better latency reference.
  dynamic "credit_specification" {
    for_each = local.is_burstable ? [1] : []
    content {
      cpu_credits = "standard"
    }
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = var.root_volume_gb
  }

  tags = merge(var.tags, { Name = "gomodel-gateway-benchmark" })
}
