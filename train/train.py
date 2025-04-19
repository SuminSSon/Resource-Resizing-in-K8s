import torch
import time
import argparse
import os
import datetime
import sys

parser = argparse.ArgumentParser()
parser.add_argument(
    "--resume-from",
    type=str,
    default="/mnt/data/checkpoints/latest.pt",
    help="Path to checkpoint file"
)
args = parser.parse_args()

def log(msg: str):
    # ISO 8601 UTC 타임스탬프 + 메시지, 버퍼 즉시 flush
    ts = datetime.datetime.utcnow().isoformat() + "Z"
    print(f"[{ts}] {msg}", flush=True)

device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
log(f"Running on device: {device}")

model = torch.nn.Linear(10, 10).to(device)
optimizer = torch.optim.Adam(model.parameters())

start_epoch = 0
if args.resume_from and os.path.exists(args.resume_from):
    log(f"Resuming from checkpoint: {args.resume_from}")
    checkpoint = torch.load(args.resume_from, map_location=device)
    model.load_state_dict(checkpoint["model"])
    optimizer.load_state_dict(checkpoint["optimizer"])
    start_epoch = checkpoint.get("epoch", 0)
else:
    log("Starting from scratch")

for epoch in range(start_epoch, 10000):
    log(f"Epoch {epoch+1}/10000 - Training ...")
    time.sleep(1)

    checkpoint_dir = os.path.dirname(args.resume_from)
    os.makedirs(checkpoint_dir, exist_ok=True)
    torch.save(
        {"epoch": epoch + 1,
         "model": model.state_dict(),
         "optimizer": optimizer.state_dict()},
        args.resume_from
    )
    log(f"Saved checkpoint to {args.resume_from}")

