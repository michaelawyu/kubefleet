kind export kubeconfig -n hub

kubectl scale deploy hub-agent --replicas=1 -n fleet-system

kubectl label membercluster kind-cluster-1 rollout-env=staging
kubectl label membercluster kind-cluster-2 rollout-env=canary
kubectl label membercluster kind-cluster-3 rollout-env=prod

kubectl create ns work-0
kubectl create configmap app -n work-0 --from-literal=foo=bar
kubectl create ns work-1
kubectl create configmap app -n work-1 --from-literal=foo=baz

kubectl apply -f example-placement-sur.yaml
kubectl apply -f example-strategy.yaml
kubectl apply -f example-run-first.yaml
