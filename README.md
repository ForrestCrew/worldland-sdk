# Worldland Node

**GPU node daemon for Worldland rental marketplace**

GPU 프로바이더로 참여하여 GPU 컴퓨팅 자원을 임대하고 수익을 얻을 수 있습니다.

## 아키텍처

```
┌─────────────────────────────────────────────────────────────────┐
│                         GPU Provider VM                          │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐ │
│  │ Node Daemon │  │   kubelet   │  │   containerd + nvidia   │ │
│  │  (Go app)   │  │ (K8s agent) │  │     runtime             │ │
│  └──────┬──────┘  └──────┬──────┘  └───────────┬─────────────┘ │
│         │                │                      │               │
│         │    mTLS        │    K8s API          │  GPU access   │
│         ▼                ▼                      ▼               │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                    NVIDIA Driver                          │  │
│  └──────────────────────────────────────────────────────────┘  │
│                              │                                  │
│                              ▼                                  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                      GPU Hardware                         │  │
│  └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                               │
                               │ Internet
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│                      Worldland Hub                               │
│  - K8s Master (control plane)                                   │
│  - mTLS 인증                                                     │
│  - 렌탈 세션 관리                                                 │
│  - 블록체인 이벤트 처리                                           │
└─────────────────────────────────────────────────────────────────┘
```

## GPU 프로바이더 필수 요구사항

GPU 노드로 참여하려면 **모든 항목**이 설치/설정되어야 합니다:

| 구성요소 | 용도 | 확인 명령어 |
|----------|------|-------------|
| NVIDIA Driver | GPU 하드웨어 접근 | `nvidia-smi` |
| NVIDIA Container Toolkit | 컨테이너에서 GPU 사용 | `nvidia-ctk --version` |
| containerd | K8s 컨테이너 런타임 | `containerd --version` |
| containerd nvidia runtime | K8s Pod에서 GPU 사용 | `cat /etc/containerd/config.toml \| grep nvidia` |
| kubeadm/kubelet | K8s 클러스터 참여 | `kubeadm version` |
| Node Daemon | Hub 통신, 등록 | `./node --help` |

**하나라도 누락되면 GPU 렌탈이 작동하지 않습니다!**

---

## 빠른 설정 가이드 (GCP Ubuntu 22.04)

### 1단계: GPU VM 생성

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

### 2단계: NVIDIA Driver 설치

```bash
# VM에 SSH 접속
gcloud compute ssh my-gpu-node --zone=us-central1-a

# Driver 설치 (535 버전 - GCP 커널과 호환)
sudo apt-get update
sudo apt-get install -y ubuntu-drivers-common
sudo ubuntu-drivers install nvidia:535

# 재부팅
sudo reboot
```

재부팅 후 확인:
```bash
nvidia-smi
# Tesla T4, 16GB 등이 표시되어야 함
```

### 3단계: NVIDIA Container Toolkit 설치

```bash
# Repository 추가
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | \
  sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
  sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
  sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

# 설치
sudo apt-get update
sudo apt-get install -y nvidia-container-toolkit
```

### 4단계: containerd 설치 및 NVIDIA 런타임 설정

```bash
# containerd 설치
sudo apt-get install -y containerd

# 기본 설정 생성
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml

# SystemdCgroup 활성화
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

# ⚠️ 중요: NVIDIA 런타임을 기본값으로 설정
sudo nvidia-ctk runtime configure --runtime=containerd --set-as-default

# containerd 재시작
sudo systemctl restart containerd
sudo systemctl enable containerd
```

**확인:**
```bash
# nvidia 런타임이 기본값인지 확인
grep -A2 'default_runtime_name' /etc/containerd/config.toml
# default_runtime_name = "nvidia" 여야 함
```

### 5단계: Kubernetes 컴포넌트 설치

```bash
# 커널 모듈 로드
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF
sudo modprobe overlay
sudo modprobe br_netfilter

# sysctl 설정
cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF
sudo sysctl --system

# swap 비활성화
sudo swapoff -a
sudo sed -i '/ swap / s/^/#/' /etc/fstab

# K8s repository 추가
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.29/deb/Release.key | \
  sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.29/deb/ /' | \
  sudo tee /etc/apt/sources.list.d/kubernetes.list

# 설치
sudo apt-get update
sudo apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl
sudo systemctl enable kubelet
```

### 6단계: Node Daemon 설치 및 실행

```bash
# Node 바이너리 다운로드 (또는 직접 빌드)
# wget https://github.com/worldland/worldland-node/releases/latest/download/node-linux-amd64
# chmod +x node-linux-amd64
# mv node-linux-amd64 node

# 지갑 개인키 설정
mkdir -p ~/.worldland && chmod 700 ~/.worldland
echo "YOUR_64_CHAR_PRIVATE_KEY" > ~/.worldland/private-key.txt
chmod 600 ~/.worldland/private-key.txt

# Node Daemon 실행
./node \
  -hub 35.193.4.15:8443 \
  -hub-http http://35.193.4.15:8080 \
  -siwe-domain 35.193.237.225:3000 \
  -private-key-file ~/.worldland/private-key.txt \
  -host $(curl -s ifconfig.me)
```

**성공 로그:**
```
Worldland Node starting...
GPU detected: Tesla T4 (15 GB)
GPU detected - will register as GPU Node
Authenticating with wallet to Hub...
SIWE authentication successful
Bootstrap certificates saved to ~/.worldland/certs
Node registered: abc123-...
Successfully joined K8s cluster
Node ready - API on port 8444
```

---

## 문제 해결

### GPU가 K8s에서 인식되지 않음

**증상:** Pod가 `Pending` 상태, 이벤트에 `Insufficient nvidia.com/gpu`

**원인:** NVIDIA Container Toolkit 미설치 또는 containerd nvidia 런타임 미설정

**해결:**
```bash
# 1. NVIDIA Container Toolkit 설치 확인
nvidia-ctk --version

# 2. containerd nvidia 런타임 설정
sudo nvidia-ctk runtime configure --runtime=containerd --set-as-default
sudo systemctl restart containerd

# 3. K8s device plugin 재시작
kubectl delete pod -n kube-system -l name=nvidia-device-plugin-ds

# 4. GPU 인식 확인
kubectl describe node $(hostname) | grep nvidia.com/gpu
# nvidia.com/gpu: 1 이 보여야 함
```

### CNI 네트워크 오류

**증상:** Pod가 `ContainerCreating`에서 멈춤, `cni0 already has an IP address` 오류

**해결:**
```bash
sudo ip link delete cni0 2>/dev/null
sudo rm -rf /etc/cni/net.d/*
sudo systemctl restart containerd kubelet
```

### SIWE 인증 실패

**증상:** `domain mismatch: expected X, got Y`

**해결:** Node의 `-siwe-domain` 플래그와 Hub의 `SIWE_DOMAIN` 환경변수가 일치해야 함

```bash
# 프론트엔드 도메인과 동일하게 설정
./node -siwe-domain 35.193.237.225:3000 ...
```

### Driver 버전 호환성

**증상:** `NVIDIA driver installation failed`, 커널 모듈 빌드 실패

**해결:** GCP 커널과 호환되는 535 버전 사용
```bash
sudo ubuntu-drivers install nvidia:535  # 590 대신 535 사용
```

---

## 명령줄 옵션

| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `-hub` | localhost:8443 | Hub mTLS 주소 |
| `-hub-http` | (자동) | Hub HTTP API URL |
| `-siwe-domain` | (자동) | SIWE 인증 도메인 |
| `-private-key-file` | - | 지갑 개인키 파일 경로 |
| `-host` | - | 외부 접속용 IP/도메인 |
| `-gpu-type` | (자동감지) | GPU 타입 (NVML에서 자동감지) |
| `-memory-gb` | (자동감지) | GPU 메모리 (NVML에서 자동감지) |
| `-price-per-sec` | 1000000000 | 초당 가격 (wei) |
| `-cert-dir` | ~/.worldland/certs | 인증서 저장 경로 |

---

## 전체 설치 체크리스트

설치 후 아래 모든 항목이 통과해야 합니다:

```bash
# 1. NVIDIA Driver
nvidia-smi
# ✓ GPU 정보 출력

# 2. NVIDIA Container Toolkit
nvidia-ctk --version
# ✓ 버전 출력 (예: 1.18.2)

# 3. containerd nvidia runtime
grep 'default_runtime_name.*nvidia' /etc/containerd/config.toml
# ✓ default_runtime_name = "nvidia"

# 4. K8s components
kubeadm version && kubelet --version
# ✓ v1.29.x

# 5. K8s GPU recognition (Node 실행 후)
kubectl describe node $(hostname) | grep 'nvidia.com/gpu'
# ✓ nvidia.com/gpu: 1

# 6. Node Daemon connected
curl -k https://localhost:8444/health
# ✓ OK
```

---

## 라이선스

MIT
