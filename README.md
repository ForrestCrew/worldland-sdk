# Worldland Node (SDK)

**GPU node daemon for the Worldland decentralized GPU rental marketplace.**

Worldland Node는 GPU 프로바이더가 자신의 머신을 Worldland 네트워크에 등록하고, GPU 임대 및 자동 채굴을 수행하는 데몬입니다.

## Architecture

```
GPU Provider Machine
+-------------------------------------------------------------+
|                                                             |
|  +------------------+     +-----------------------------+   |
|  |   Node Daemon    |     |   Docker Engine (nvidia)    |   |
|  |                  |     |                             |   |
|  |  - SIWE Auth     |     |  +---------+  +---------+  |   |
|  |  - mTLS Client   +---->|  | Mining  |  | Rental  |  |   |
|  |  - Heartbeat     |     |  | (idle)  |  | (SSH)   |  |   |
|  |  - Rental Exec   |     |  +---------+  +---------+  |   |
|  |  - Mining Daemon  |     |                             |   |
|  +--------+---------+     +-------------+---------------+   |
|           |                             |                   |
|           |                     +-------+-------+           |
|           |                     | NVIDIA Driver |           |
|           |                     +-------+-------+           |
|           |                             |                   |
|           |                     +-------+-------+           |
|           |                     | GPU Hardware  |           |
|           |                     +---------------+           |
+-----------+---------------------------------------------+---+
            |
            | mTLS (port 8443)
            v
+-------------------------------------------------------------+
|                      Worldland Hub                          |
|  - REST API + mTLS server                                   |
|  - Rental session management                                |
|  - Blockchain event listener (Sepolia)                      |
|  - PostgreSQL                                               |
+-------------------------------------------------------------+
```

## Features

- **SIWE Wallet Authentication**: Ethereum 지갑 기반 인증 (Sign-In with Ethereum)
- **mTLS Communication**: Hub과의 상호 TLS 인증 통신
- **Docker Rental Execution**: 임대 요청 시 GPU Docker 컨테이너 생성 (SSH 접속 제공)
- **Auto Mining**: 유휴 GPU로 자동 채굴, 임대 시 자동 중단/재개
- **GPU Auto-detection**: NVML을 통한 GPU 타입/메모리 자동 감지
- **Heartbeat**: 30초 간격 상태 보고 (GPU 메트릭, 채굴 상태)

## Requirements

| 구성요소 | 용도 | 확인 명령어 |
|----------|------|-------------|
| NVIDIA Driver | GPU 하드웨어 접근 | `nvidia-smi` |
| Docker Engine | 컨테이너 런타임 | `docker --version` |
| NVIDIA Container Toolkit | Docker에서 GPU 사용 | `nvidia-ctk --version` |
| Node Daemon | Hub 통신 + 임대/채굴 실행 | `./node-linux --help` |

> K8s(kubeadm, kubelet 등)는 **불필요**합니다. Docker만 있으면 됩니다.

## Quick Start

### 1. GPU VM 생성 (GCP 예시)

```bash
gcloud compute instances create my-gpu-node \
  --zone=us-central1-a \
  --machine-type=n1-standard-4 \
  --accelerator=type=nvidia-tesla-t4,count=1 \
  --maintenance-policy=TERMINATE \
  --image-family=ubuntu-2204-lts \
  --image-project=ubuntu-os-cloud \
  --boot-disk-size=100GB
```

### 2. NVIDIA Driver 설치

```bash
sudo apt-get update
sudo apt-get install -y ubuntu-drivers-common
sudo ubuntu-drivers install nvidia:535
sudo reboot
```

재부팅 후 확인:
```bash
nvidia-smi
# Tesla T4, 15360 MiB 등이 표시되어야 함
```

### 3. Docker + NVIDIA Container Toolkit 설치

```bash
# Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
newgrp docker

# NVIDIA Container Toolkit
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | \
  sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
  sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
  sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

sudo apt-get update
sudo apt-get install -y nvidia-container-toolkit

# Docker에 NVIDIA 런타임 등록
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

확인:
```bash
docker run --rm --gpus all nvidia/cuda:12.2.0-base-ubuntu22.04 nvidia-smi
# GPU 정보가 출력되어야 함
```

### 4. Node Daemon 빌드 및 실행

```bash
# 빌드 (Go 1.21+)
cd worldland-node
GOOS=linux GOARCH=amd64 go build -o node-linux ./cmd/node/

# 실행
./node-linux \
  -hub <HUB_IP>:8443 \
  -hub-http http://<HUB_IP>:8080 \
  -host $(curl -s ifconfig.me) \
  -private-key <ETHEREUM_PRIVATE_KEY> \
  -siwe-domain <FRONTEND_DOMAIN>:3000 \
  -enable-mining=true
```

**성공 로그:**
```
Worldland Node starting...
GPU detected: Tesla T4 (15 GB)
GPU detected - will register as GPU Node
Authenticating with wallet to Hub...
SIWE authentication successful
Bootstrap certificates saved to ~/.worldland/certs
Node registered: d2250be4-2412-4166-9207-658b193a4a8c
Mining daemon initialized: image=mingeyom/worldland-mio:latest gpus=1
Connected to Hub via mTLS
Node daemon running (Docker rental executor enabled)
Mining daemon started in background
Node ready - API on port 8444
```

### 5. systemd 서비스 등록 (선택)

```bash
sudo tee /etc/systemd/system/worldland-node.service << 'EOF'
[Unit]
Description=Worldland GPU Node
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=ahwlsqja
ExecStart=/home/ahwlsqja/node-linux \
  -hub <HUB_IP>:8443 \
  -hub-http http://<HUB_IP>:8080 \
  -host <PUBLIC_IP> \
  -private-key <PRIVATE_KEY> \
  -siwe-domain <FRONTEND_DOMAIN>:3000 \
  -enable-mining=true
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now worldland-node
```

## How It Works

### Rental Flow

1. 사용자가 Frontend에서 GPU 임대 요청
2. Hub이 블록체인 트랜잭션 확인 후 Node에 `start_rental` mTLS 명령 전송
3. Node가 Docker 컨테이너 생성 (GPU + SSH 접속 설정)
4. 사용자가 SSH로 컨테이너에 접속하여 GPU 사용
5. 임대 종료 시 Hub이 `stop_rental` 명령 전송 → 컨테이너 정리

### Mining

- 노드 시작 시 유휴 GPU로 자동 채굴 시작 (`mingeyom/worldland-mio`)
- 임대 요청이 오면 자동으로 채굴 중단 → GPU를 임대 컨테이너에 할당
- 임대 종료 시 자동으로 채굴 재개
- `-enable-mining=false`로 비활성화 가능

### Heartbeat

- 30초마다 Hub에 상태 보고 (mTLS 연결 통해)
- GPU 메트릭 (사용률, 온도, 메모리)
- 채굴 상태 (running/paused/stopped, container ID, GPU count)
- Hub 대시보드에서 실시간 모니터링 가능

## CLI Options

| Flag | Default | Description |
|------|---------|-------------|
| `-hub` | `localhost:8443` | Hub mTLS 서버 주소 |
| `-hub-http` | (auto) | Hub REST API URL |
| `-host` | - | 외부 접속 IP (SSH 접속용, 필수) |
| `-private-key` | - | Ethereum 지갑 개인키 (hex) |
| `-private-key-file` | - | 개인키 파일 경로 |
| `-siwe-domain` | (auto) | SIWE 인증 도메인 |
| `-enable-mining` | `true` | 자동 채굴 활성화 |
| `-mining-image` | `mingeyom/worldland-mio:latest` | 채굴 Docker 이미지 |
| `-mining-data-dir` | `/data/worldland` | 채굴 블록체인 데이터 경로 |
| `-api-port` | `8444` | Node mTLS API 포트 |
| `-cert-dir` | `~/.worldland/certs` | 인증서 저장 경로 |
| `-gpu-type` | (auto-detect) | GPU 타입 (NVML 자동감지) |
| `-memory-gb` | (auto-detect) | GPU 메모리 GB (NVML 자동감지) |
| `-price-per-sec` | `1000000000` | 초당 임대 가격 (wei) |

## Supported GPU Images

임대 시 사용자가 선택할 수 있는 기본 이미지:

| Image | Size | Description |
|-------|------|-------------|
| `nvidia/cuda:12.6.0-devel-ubuntu22.04` | ~11 GB | CUDA 개발 환경 |
| `pytorch/pytorch:2.6.0-cuda12.6-cudnn9-devel` | ~20 GB | PyTorch + CUDA |

> 큰 이미지는 사전 Pull을 권장합니다: `docker pull <image>`

## Troubleshooting

### SIWE 인증 실패

**증상:** `domain mismatch: expected X, got Y`

**해결:** `-siwe-domain` 값이 Hub의 `SIWE_DOMAIN` 환경변수와 일치해야 합니다.

### Hub 재시작 후 연결 끊김

**증상:** `Failed to send heartbeat: use of closed network connection`

**해결:** Hub 재시작 시 Node도 재시작해야 합니다. mTLS 연결은 자동 재접속을 지원하지 않습니다.

### Docker GPU 접근 불가

**증상:** `could not select device driver "nvidia" with capabilities: [[gpu]]`

**해결:**
```bash
# NVIDIA Container Toolkit 재설정
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker

# 확인
docker run --rm --gpus all nvidia/cuda:12.2.0-base-ubuntu22.04 nvidia-smi
```

### 이미지 Pull 타임아웃

**증상:** `context deadline exceeded` during image pull

**해결:** 대형 이미지(PyTorch 등)는 사전에 Pull:
```bash
docker pull pytorch/pytorch:2.6.0-cuda12.6-cudnn9-devel
```

## Project Structure

```
worldland-node/
  cmd/node/          # Entrypoint (main.go)
  internal/
    adapters/
      mtls/          # mTLS client (Hub 연결)
      nvml/          # NVIDIA GPU 감지 (NVML)
    api/             # Rental API handler (mTLS)
    auth/            # SIWE wallet authentication
    container/       # Docker service (GPU container lifecycle)
    mining/          # Mining daemon (auto-start/pause/resume)
    rental/          # Rental executor (port allocation, container management)
    services/        # Node daemon (command dispatch, heartbeat)
```

## License

MIT
