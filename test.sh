# 1. MLJob 생성
kubectl apply -f standby-mljob.yaml

# 2. 60초 대기
sleep 90

# 3. CPU 10으로 패치
kubectl -n sumin patch mljob resize-demo --type=merge -p '{"spec":{"cpu":"10"}}'
