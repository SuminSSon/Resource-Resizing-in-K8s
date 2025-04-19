# 컨트롤러 배포
kubectl apply -f mljob-controller-rbac.yaml
kubectl apply -f mljob-controller-clusterrole.yaml
kubectl apply -f mljob-controller-deployment.yaml

# CRD 
kubectl apply -f mljob-crd.yaml

# LocalQueue
kubectl apply -f localqueue.yaml

# PVC
kubectl apply -f checkpoint-pvc.yaml
kubectl apply -f checkpoint-pv.yaml

# ClusterQueue
kubectl apply -f clusterqueue.yaml

# MLJob 실행
kubectl apply -f mljob.yaml
