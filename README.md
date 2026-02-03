# Worldland Node

**GPU node daemon for Worldland rental marketplace**

The Node daemon manages GPU containers via Docker SDK, communicates with Hub via mTLS, and provides SSH access to rental sessions.

## Quick Start (5 minutes)

Requires: Docker with NVIDIA Container Toolkit, mTLS certificates

```bash
# Build Node
go build -o node ./cmd/node

# Run Node (replace values)
./node \
  -hub localhost:8443 \
  -node-id YOUR_NODE_ID \
  -cert node.crt \
  -key node.key \
  -ca ca.crt \
  -host your-public-hostname.com
```

## Prerequisites

**Required:**
- Go 1.21.0 or higher ([download](https://go.dev/dl/))
- Docker 20.10+ ([download](https://docs.docker.com/get-docker/))
- NVIDIA GPU with drivers installed
- NVIDIA Container Toolkit

**Check versions:**
```bash
go version           # Should be go1.21.0 or higher
docker --version     # Should be 20.10 or higher
nvidia-smi           # Should show your GPU(s)
```

## Installation

### 1. Install NVIDIA Container Toolkit

The NVIDIA Container Toolkit enables Docker to access GPU hardware.

**Ubuntu/Debian:**
```bash
# Add NVIDIA package repository
distribution=$(. /etc/os-release;echo $ID$VERSION_ID)
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

curl -s -L https://nvidia.github.io/libnvidia-container/$distribution/libnvidia-container.list | \
  sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
  sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

# Install toolkit
sudo apt-get update
sudo apt-get install -y nvidia-container-toolkit

# Configure Docker
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

**Verify GPU access in Docker:**
```bash
docker run --rm --gpus all nvidia/cuda:12.1-base nvidia-smi
```

You should see your GPU(s) listed. If this fails, check NVIDIA drivers and Container Toolkit installation.

### 2. Install Go dependencies

```bash
cd worldland-node
go mod download
```

### 3. Build Node

```bash
go build -o node ./cmd/node
```

### 4. Generate mTLS Certificates

Nodes communicate with Hub using mTLS (mutual TLS). You need:
- CA certificate (from Hub's step-ca)
- Node certificate and private key

**Option A: Get certificates from Hub administrator**

If running in a production environment, request certificates from the Hub administrator.

**Option B: Generate from step-ca (development)**

Ensure step-ca is running (from worldland-hub):
```bash
cd ../worldland-hub
docker-compose up -d step-ca

# Wait for healthy status
docker-compose ps
```

Generate Node certificate:
```bash
# Bootstrap step CLI with the CA
step ca bootstrap --ca-url https://localhost:9000 --fingerprint <ROOT_FINGERPRINT>

# Get certificate
step ca certificate node.worldland.io node.crt node.key
```

Note: The root fingerprint is shown when step-ca starts. Check logs with `docker-compose logs step-ca`.

**Option C: Development without mTLS**

For local development, the Hub can run without mTLS verification. See Hub README for dev mode configuration.

### 5. Register Node with Hub

Before running the Node, you need a Node ID from Hub:

```bash
# Register node via Hub API (requires SIWE authentication)
# The response includes your node_id
curl -X POST http://localhost:8080/api/v1/nodes/register \
  -H "Authorization: Bearer YOUR_SESSION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "wallet_address": "0xYourWalletAddress",
    "host_address": "your-public-hostname.com"
  }'
```

Save the returned `node_id` for the next step.

## Running the Node

### Command-Line Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `-hub` | No | localhost:8443 | Hub mTLS address |
| `-api-port` | No | 8444 | Node API port |
| `-host` | Yes | (none) | Public hostname for SSH |
| `-cert` | Yes | node.crt | Node certificate file |
| `-key` | Yes | node.key | Node private key file |
| `-ca` | Yes | ca.crt | CA certificate file |
| `-node-id` | Yes | (none) | Node ID from registration |

### Development (local Hub)

```bash
./node \
  -hub localhost:8443 \
  -node-id abc123 \
  -cert node.crt \
  -key node.key \
  -ca ca.crt \
  -host localhost
```

### Production

```bash
./node \
  -hub hub.worldland.io:8443 \
  -node-id YOUR_NODE_ID \
  -cert /etc/worldland/node.crt \
  -key /etc/worldland/node.key \
  -ca /etc/worldland/ca.crt \
  -host gpu-node-1.yourcompany.com \
  -api-port 8444
```

**Verify:**
Node should log:
```
Worldland Node starting...
Connected to Hub at hub.worldland.io:8443
Detected GPUs: [NVIDIA GeForce RTX 4090]
Node ready, listening on :8444
```

## GPU Detection

Node automatically detects NVIDIA GPUs using NVML (NVIDIA Management Library).

**Mock mode:** If NVML is not available (no NVIDIA GPU), Node uses a mock GPU provider for development.

**List detected GPUs:**
```bash
# Check what Node sees
./node -node-id test -cert node.crt -key node.key -ca ca.crt
# Look for "Detected GPUs" in startup logs
```

## Project Structure

```
worldland-node/
├── cmd/
│   └── node/              # Node binary entry point
├── internal/
│   ├── adapters/
│   │   └── nvml/          # NVIDIA GPU detection
│   ├── api/               # mTLS API handlers
│   ├── container/         # Docker container management
│   ├── domain/            # Domain models (GPU specs, rentals)
│   ├── port/              # Dynamic SSH port allocation
│   ├── rental/            # Rental session management
│   └── services/          # Service orchestration
└── go.mod                 # Go dependencies
```

## Troubleshooting

### Error: "NVML not available, using mock provider"

**Cause:** NVIDIA Container Toolkit not installed or GPU not detected.

**Solution:**
```bash
# Verify NVIDIA driver
nvidia-smi

# Verify Container Toolkit
docker run --rm --gpus all nvidia/cuda:12.1-base nvidia-smi

# If fails, reinstall Container Toolkit (see Installation)
```

### Error: "Failed to load certificate"

**Cause:** Certificate files not found or invalid.

**Solution:**
```bash
# Check files exist
ls -la node.crt node.key ca.crt

# Verify certificate format
openssl x509 -in node.crt -text -noout
```

### Error: "Failed to connect to Hub"

**Cause:** Hub not running, wrong address, or certificate mismatch.

**Solution:**
```bash
# Check Hub is running
curl http://localhost:8080/health

# Verify certificates match Hub's CA
# The ca.crt must be from the same step-ca that issued Hub's certificate
```

### Error: "node-id is required"

**Cause:** Missing `-node-id` flag.

**Solution:** Register with Hub first to get a node ID (see Step 5 in Installation).

### Docker GPU access fails

```bash
# Check Docker daemon can access GPU runtime
docker info | grep -i nvidia

# If missing, configure runtime
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

## Security Considerations

1. **mTLS Required:** All Hub-Node communication uses mutual TLS. Never expose Node API without mTLS.

2. **Certificate Storage:** Store certificates in a secure location with restricted permissions:
   ```bash
   chmod 600 node.key
   chmod 644 node.crt ca.crt
   ```

3. **Network:** Only expose the SSH ports to renters. The Node API (8444) should only be accessible to Hub.

## License

MIT
