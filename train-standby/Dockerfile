FROM python:3.10-slim

RUN pip install --no-cache-dir torch torchvision

WORKDIR /app
COPY train_standby.py .

VOLUME ["/mnt/data/checkpoints"]

ENTRYPOINT ["python", "train_standby.py"]

