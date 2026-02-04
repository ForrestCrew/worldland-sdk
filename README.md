# Worldland Node

**GPU node daemon for Worldland rental marketplace**

The Node daemon manages GPU containers via Docker SDK, communicates with Hub via mTLS, and provides SSH access to rental sessions.

## Quick Start (3 minutes)

Requires: Docker with NVIDIA Container Toolkit, Ethereum wallet

```bash
# Build Node
go build -o node ./cmd/node

# Set up wallet private key
mkdir -p ~/.worldland && chmod 700 ~/.worldland
echo "your_64_char_private_key" > ~/.worldland/private-key.txt
chmod 600 ~/.worldland/private-key.txt

# Run Node (certificates auto-provisioned!)
./node \
  -hub hub.worldland.io:8443 \
  -private-key-file ~/.worldland/private-key.txt \
  -host $(curl -s ifconfig.me)
```

That's it! Certificates are automatically issued and saved to `~/.worldland/certs/`.

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

## Wallet Authentication (SIWE) & Auto-Bootstrap

Worldland Node supports **Sign-In with Ethereum (SIWE)** for provider registration and **automatic certificate provisioning**. This links your GPU node to your blockchain wallet address, enabling:

- Automatic provider registration with real wallet address
- **Automatic mTLS certificate issuance** (no manual cert setup!)
- Direct settlement of rental payments to your wallet
- On-chain identity verification

### How Auto-Bootstrap Works

On first run with a private key, the Node automatically:

```
1. Login with SIWE (wallet authentication)
2. Request bootstrap certificate from Hub
3. Save certificates to ~/.worldland/certs/
4. Connect via mTLS
5. Register GPU node
```

### Setting Up Wallet Authentication

1. **Get your Ethereum private key**

   Export your private key from MetaMask or another wallet. The key should be a 64-character hex string.

   **Security Warning:** Never share your private key. Store it securely with restricted permissions.

2. **Create a private key file**
   ```bash
   # Create secure directory
   mkdir -p ~/.worldland
   chmod 700 ~/.worldland

   # Save private key (without 0x prefix)
   echo "your_64_char_hex_private_key" > ~/.worldland/private-key.txt
   chmod 600 ~/.worldland/private-key.txt
   ```

3. **Run Node** (certificates auto-provisioned!)
   ```bash
   ./node \
     -hub hub.worldland.io:8443 \
     -private-key-file ~/.worldland/private-key.txt \
     -host your-public-ip.com \
     -gpu-type "NVIDIA RTX 4090" \
     -memory-gb 24 \
     -price-per-sec "1000000000"
   ```

**First run output:**
```
Worldland Node starting...
Authenticating with wallet to Hub at http://hub.worldland.io:8080...
Wallet address: 0xYourWalletAddress
SIWE authentication successful
Certificates not found, requesting bootstrap certificate from Hub...
Bootstrap certificates saved to /home/user/.worldland/certs
  Certificate: /home/user/.worldland/certs/node.crt
  Private Key: /home/user/.worldland/certs/node.key
  CA Cert: /home/user/.worldland/certs/ca.crt
  Expires: 2026-03-06T12:00:00Z
Node registered: node_abc123
Connected to Hub at hub.worldland.io:8443
Node ready - API on port 8444
```

**Subsequent runs** use the saved certificates automatically.

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

### 5. Set Up Wallet Authentication (Recommended)

With wallet authentication, Node automatically registers with Hub on startup.

```bash
# Create secure directory for private key
mkdir -p ~/.worldland
chmod 700 ~/.worldland

# Save your Ethereum private key (64 hex characters, no 0x prefix)
echo "your_private_key_here" > ~/.worldland/private-key.txt
chmod 600 ~/.worldland/private-key.txt
```

**Alternative: Manual Registration (Development)**

If not using wallet authentication, register manually via Hub API:

```bash
# Register node via Hub API (requires SIWE authentication)
curl -X POST http://localhost:8080/api/v1/nodes/register \
  -H "Authorization: Bearer YOUR_SESSION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "wallet_address": "0xYourWalletAddress",
    "host_address": "your-public-hostname.com"
  }'
```

Save the returned `node_id` and use with `-node-id` flag.

## Running the Node

### Command-Line Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `-hub` | No | localhost:8443 | Hub mTLS address |
| `-hub-http` | No | (derived) | Hub HTTP API URL for authentication |
| `-api-port` | No | 8444 | Node API port |
| `-host` | Yes | (none) | Public hostname for SSH |
| `-cert` | Yes | node.crt | Node certificate file |
| `-key` | Yes | node.key | Node private key file |
| `-ca` | Yes | ca.crt | CA certificate file |
| `-node-id` | Conditional | (auto) | Node ID (auto-assigned with wallet auth) |
| `-private-key` | No | (none) | Ethereum private key (hex) |
| `-private-key-file` | No | (none) | Path to file containing private key |
| `-gpu-type` | No | NVIDIA RTX 4090 | GPU type for registration |
| `-memory-gb` | No | 24 | GPU memory in GB |
| `-price-per-sec` | No | 1000000000 | Price per second in wei |

**Note:** Either provide `-private-key` or `-private-key-file` for wallet authentication. If neither is provided, the Node runs in mock mode (development only) and requires `-node-id`.

### Development (local Hub)

**With wallet authentication:**
```bash
./node \
  -hub localhost:8443 \
  -hub-http http://localhost:8080 \
  -private-key-file ~/.worldland/private-key.txt \
  -cert node.crt \
  -key node.key \
  -ca ca.crt \
  -host localhost \
  -gpu-type "Mock GPU" \
  -memory-gb 24
```

**Without wallet (mock mode):**
```bash
./node \
  -hub localhost:8443 \
  -node-id test-node-123 \
  -cert node.crt \
  -key node.key \
  -ca ca.crt \
  -host localhost
```

### Production

```bash
./node \
  -hub hub.worldland.io:8443 \
  -hub-http https://hub.worldland.io:8080 \
  -private-key-file /etc/worldland/private-key.txt \
  -cert /etc/worldland/node.crt \
  -key /etc/worldland/node.key \
  -ca /etc/worldland/ca.crt \
  -host gpu-node-1.yourcompany.com \
  -api-port 8444 \
  -gpu-type "NVIDIA RTX 4090" \
  -memory-gb 24 \
  -price-per-sec "1000000000"
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
│   ├── auth/              # SIWE wallet authentication
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

**Cause:** Missing `-node-id` flag and no wallet authentication configured.

**Solution:** Either:
1. Use wallet authentication with `-private-key-file` (recommended)
2. Or provide `-node-id` for mock mode

### Error: "SIWE authentication failed"

**Cause:** Wallet authentication failed with Hub.

**Solution:**
```bash
# Check Hub HTTP API is accessible
curl http://hub.worldland.io:8080/health

# Verify private key format (64 hex chars, no 0x prefix)
cat ~/.worldland/private-key.txt | wc -c  # Should be 64 or 65

# Check Hub's SIWE domain configuration
# Hub must have SIWE_DOMAIN set to match the connection
```

### Error: "Failed to create SIWE client: invalid private key"

**Cause:** Private key is malformed.

**Solution:**
```bash
# Private key should be 64 hex characters
# Remove 0x prefix if present
# Remove any whitespace or newlines

# Verify key format
cat ~/.worldland/private-key.txt | tr -d '\n' | wc -c  # Should be 64
```

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
