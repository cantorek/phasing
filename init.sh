rm -f ${HOME}/.ssh/phasing_key ${HOME}/.ssh/phasing_key.pub
ssh-keygen -b 2048 -t rsa -f ${HOME}/.ssh/phasing_key -N ""
kubectl delete configmap phasing-config
kubectl create configmap phasing-config --from-file=authorized_keys=${HOME}/.ssh/phasing_key.pub
kubectl delete pod phasing
kubectl apply -f phasing.yaml
kubectl port-forward pod/phasing 2222:22
