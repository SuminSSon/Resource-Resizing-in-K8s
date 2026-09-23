# Resource Resizing in Kubernetes

Kubernetes 환경에서 실행 중인 ML 학습 작업의 자원 요구량이 변경될 때, **Kueue 기반 자원 승인과 checkpoint를 이용해 새로운 자원 구성으로 전환하는 MLJob Controller**입니다.

기존 Pod를 즉시 종료하고 다시 생성하는 대신, 새로운 자원을 사용할 수 있는지 먼저 확인한 뒤 새로운 Pod를 준비하고 기존 학습 상태를 checkpoint로 저장하여 작업을 전환합니다.

## Overview

사용자는 `MLJob` Custom Resource를 통해 학습 이미지, CPU 자원, checkpoint 경로, Kueue queue 등을 정의합니다.

Controller는 `MLJob`의 상태를 지속적으로 확인하며 다음 과정을 관리합니다.

```text
MLJob 생성
    │
    ▼
Active Workload 생성
    │
    ▼
Kueue Admission
    │
    ▼
Training Pod 실행
    │
    │ Resource 변경
    ▼
Standby Workload 생성
    │
    ▼
Kueue Admission 대기
    │
    ▼
Standby Pod 준비
    │
    ▼
Active Pod Checkpoint
    │
    ▼
Active Workload 종료
    │
    ▼
Standby → Active 전환
```

## Resource Resizing

실행 중 `MLJob.spec.cpu`가 변경되면 Controller는 현재 Workload의 CPU와 새로 요청된 CPU를 비교합니다.

자원 변경이 감지되면 기존 학습 작업을 바로 종료하지 않고 새로운 자원 요구량을 가진 **Standby Workload**를 생성합니다.

Kueue가 Standby Workload의 자원 요청을 승인할 때까지 기존 Active Workload는 계속 실행됩니다.

```text
Active Workload
CPU: 5
Training
      │
      │ CPU 5 → 10
      ▼
Standby Workload
CPU: 10
      │
      ▼
Kueue Admission
```

새로운 자원이 확보되고 Standby Pod가 준비되면 기존 Active Pod에 `SIGUSR1`을 전달하여 checkpoint 저장을 요청합니다.

Checkpoint는 Persistent Volume에 저장되며, 이후 기존 Active Workload와 Pod를 종료하고 Standby Workload를 새로운 Active Workload로 전환합니다.

## Components

- **MLJob CRD**  
  학습 이미지, CPU, checkpoint 경로, queue 등의 작업 설정을 정의합니다.

- **MLJob Controller**  
  MLJob의 생성 및 변경을 감지하고 Workload와 Pod의 lifecycle을 관리합니다.

- **Kueue**  
  요청된 자원을 사용할 수 있는지 확인하고 Workload admission을 관리합니다.

- **Active Workload**  
  현재 학습을 수행하고 있는 Workload입니다.

- **Standby Workload**  
  변경된 자원으로 실행하기 위해 미리 생성되는 Workload입니다.

- **Persistent Volume**  
  기존 학습 상태를 저장하고 새로운 학습 프로세스에서 재사용하기 위한 checkpoint 저장소입니다.

## MLJob Example

```yaml
apiVersion: ai.mljob-controller/v1
kind: MLJob
metadata:
  name: mljob-train-sumin
  namespace: sumin
spec:
  image: itoodo12/train:v5
  cpu: "5"
  checkpointPVC: checkpoint-pvc
  checkpointPath: /mnt/data/checkpoints
  queueName: training-queue
```

CPU 자원을 변경하려면 `spec.cpu` 값을 수정합니다.

```yaml
spec:
  cpu: "10"
```

Controller가 변경을 감지하면 새로운 자원 구성을 위한 Standby Workload를 생성하고 전환 과정을 수행합니다.

## Project Structure

```text
.
├── api/v1/
│   └── mljob_types.go
├── main.go
├── mljob-crd.yaml
├── mljob.yaml
├── clusterqueue.yaml
├── localqueue.yaml
├── resourceflavor.yaml
├── checkpoint-pv.yaml
├── checkpoint-pvc.yaml
├── mljob-controller-deployment.yaml
├── mljob-controller-rbac.yaml
├── train/
└── train-standby/
```

`main.go`에는 MLJob reconciliation 및 Active–Standby 전환 로직이 구현되어 있으며, `train-standby/`에는 checkpoint 저장 및 복구를 확인하기 위한 학습 코드가 포함되어 있습니다.

## Setup

Controller, CRD, Kueue queue 및 checkpoint volume을 생성합니다.

```bash
./run.sh
```

이후 MLJob을 생성합니다.

```bash
kubectl apply -f mljob.yaml
```

MLJob 상태 및 생성된 Workload를 확인할 수 있습니다.

```bash
kubectl get mljob -n sumin
kubectl get workloads -n sumin
kubectl get pods -n sumin
```

## Current Scope

현재 구현은 **CPU resource resizing과 checkpoint 기반 Active–Standby 전환을 검증하기 위한 prototype**입니다.

GPU 필드는 CRD에 정의되어 있지만 현재 Active–Standby 전환 경로의 자원 할당은 CPU를 중심으로 구현되어 있습니다.
