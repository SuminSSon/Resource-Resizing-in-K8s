#!/usr/bin/env python3
import os, glob, signal, torch, time
import torch.nn as nn
import torch.optim as optim
from torchvision import datasets, transforms
from datetime import datetime

# -------------------- 기본 경로 --------------------
ROOT_DIR      = "/mnt/data/checkpoints"       # PVC 한 곳
os.makedirs(ROOT_DIR, exist_ok=True)

TS            = datetime.utcnow().strftime("%Y-%m-%dT%H-%M-%S")
LOG_PATH      = os.path.join(ROOT_DIR, f"{TS}_train.log")

CKPT_PATTERN  = os.path.join(ROOT_DIR, "checkpoint_*.pt")
KEEP_LAST     = 10          # 보존 개수
SAVE_EVERY    = 512         # 배치 저장 주기

# -------------------- 로깅 -------------------------
def now():
    return datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%S.%fZ")  # μs 포함

def log(msg):
    line = f"{now()} {msg}"
    print(line, flush=True)
    with open(LOG_PATH, "a") as f:
        f.write(line + "\n")

# -------------------- 모델 -------------------------
class SimpleNet(nn.Module):
    def __init__(self):
        super().__init__()
        self.fc = nn.Linear(28*28, 10)
    def forward(self, x):
        return self.fc(x.view(-1, 28*28))

# -------------------- 체크포인트 --------------------
def save_ckpt(model, opt, step):
    path = os.path.join(ROOT_DIR, f"checkpoint_{step:09d}.pt")
    torch.save({"step": step,
                "model": model.state_dict(),
                "optim": opt.state_dict()}, path)
    log(f"[CKPT] saved → {os.path.basename(path)}")

    # 로테이션: 최신 KEEP_LAST 개만 남김
    files = sorted(glob.glob(CKPT_PATTERN))
    for old in files[:-KEEP_LAST]:
        os.remove(old)

def load_latest(model, opt):
    files = sorted(glob.glob(CKPT_PATTERN))
    if not files:
        log("[CKPT] none found, fresh start")
        return 0
    latest = files[-1]
    data   = torch.load(latest, map_location="cpu")
    model.load_state_dict(data["model"])
    opt.load_state_dict(data["optim"])
    log(f"[CKPT] restored ← {os.path.basename(latest)}")
    return data["step"] + 1   # 다음 스텝부터

# -------------------- SIGUSR1 핸들러 ----------------
model, optimizer, global_step = None, None, 0
def on_usr1(sig, frame):
    if model and optimizer:
        log(f"[SIGUSR1] manual checkpoint at step {global_step}")
        save_ckpt(model, optimizer, global_step)
signal.signal(signal.SIGUSR1, on_usr1)

# -------------------- 학습 -------------------------
def train():
    global model, optimizer, global_step

    log("[BOOT] container started")

    tfm = transforms.Compose([
        transforms.ToTensor(),
        transforms.Normalize((0.1307,), (0.3081,))
    ])
    ds  = datasets.MNIST("/mnt/data", train=True, download=True, transform=tfm)
    dl  = torch.utils.data.DataLoader(ds, batch_size=64, shuffle=True)

    model      = SimpleNet()
    optimizer  = optim.SGD(model.parameters(), lr=0.01)
    criterion  = nn.CrossEntropyLoss()

    global_step = load_latest(model, optimizer)
    log(f"[TRAIN] starting at step {global_step}, save_every={SAVE_EVERY}")

    first_batch_logged = False
    for epoch in range(20):
        for batch_idx, (x, y) in enumerate(dl):
            if not first_batch_logged:
                log("[TRAIN] first batch started")
                first_batch_logged = True

            optimizer.zero_grad()
            loss = criterion(model(x), y)
            loss.backward()
            optimizer.step()

            if global_step % SAVE_EVERY == 0:
                save_ckpt(model, optimizer, global_step)

            if global_step % 100 == 0:
                log(f"[STEP {global_step}] loss={loss.item():.4f}")

            global_step += 1
    log("[EVAL] evaluating final model accuracy")

    test_ds = datasets.MNIST("/mnt/data", train=False, download=True, transform=tfm)
    test_dl = torch.utils.data.DataLoader(test_ds, batch_size=64, shuffle=False)

    model.eval()
    correct = 0
    total = 0
    with torch.no_grad():
        for x, y in test_dl:
            outputs = model(x)
            _, predicted = torch.max(outputs.data, 1)
            total += y.size(0)
            correct += (predicted == y).sum().item()

    acc = correct / total * 100
    log(f"[DONE] training finished at {now()} (step {global_step}) - Accuracy: {acc:.2f}%")

if __name__ == "__main__":
    train()

