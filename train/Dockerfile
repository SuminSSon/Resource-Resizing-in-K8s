FROM python:3.10-slim
WORKDIR /app
COPY train.py .
RUN pip install numpy torch
RUN mkdir -p /mnt/data/checkpoints

ENTRYPOINT ["python", "train.py"]
CMD ["--resume-from=/mnt/data/checkpoints/latest.pt"] 
