kubectl delete deployment --all
kubectl delete secret --all
kubectl delete configmap --all
export APP=publisher
export TARGET=kubernetes
export REPO=gcr.io/chao1-1084
make clean
make secret
kubectl create -f /usr/local/google/home/xuchao/go-workspace/src/k8s.io/contrib/mungegithub/publisher/local.secret.yaml
kubectl create -f /usr/local/google/home/xuchao/go-workspace/src/k8s.io/contrib/mungegithub/publisher/deployment/kubernetes/configmap.yaml
make deploy
