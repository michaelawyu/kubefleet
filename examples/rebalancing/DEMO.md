* Change the directory:

    
    ```sh
    # cd ~/Workplace/kubefleet-fork/examples/rebalancing
    cd examples/rebalancing
    ```

* Switch to the hub cluster context

    ```sh
    kind export kubeconfig --name=hub
    ```

* Create the namespaces:

    ```sh
    kubectl create namespace system-a1
    kubectl create namespace system-a2
    kubectl create namespace app
    ```

* Create the config maps:

    ```sh
    kubectl create configmap cm-a1 -n app --from-literal=foo=bar
    kubectl create configmap cm-a2 -n app --from-literal=foo=bar
    ```

* Create the deployment:

    ```sh
    kubectl apply -f deploy-busybox.yaml
    ```

* Label the member clusters:

    ```sh
    kubectl label cluster kind-cluster-1 env=red --overwrite
    kubectl label cluster kind-cluster-2 env=blue --overwrite
    kubectl label cluster kind-cluster-3 env=yellow --overwrite
    ```

* Add ValidatingAdmissionPolicy and ValidatingAdmissionPolicyBinding to block pods with images containing "busybox" on `cluster-3`:

    ```sh
    kind export kubeconfig -n cluster-3
    kubectl apply -f validatingpolicy.yaml
    kind export kubeconfig --name=hub
    ```

* Create the placements:

    ```sh
    kubectl apply -f crp-all.yaml
    kubectl apply -f crp-a1.yaml
    kubectl apply -f crp-a2.yaml
    kubectl apply -f rp-a1.yaml
    kubectl apply -f rp-a2.yaml
    ```

* Taint the `cluster-1`:

    ```sh
    kubectl patch membercluster kind-cluster-1 --type=json -p='[{"op":"add","path":"/spec/taints","value":[{"key":"foo","value":"bar","effect":"NoSchedule"}]}]'
    ```

    To drop the taint:

    ```sh
    kubectl patch membercluster kind-cluster-1 --type=json -p='[{"op":"remove","path":"/spec/taints"}]'
    ```

* Apply the first cluster rebalancing request:

    ```sh
    kubectl apply -f rebalancing-req-1.yaml
    ```

* Check the status of the first cluster rebalancing request:

    ```sh
    kubectl get clusterrebalancingrequest example-1 -o yaml
    ```

* After the first request is completed, commit the changes:

    ```sh
    kubectl delete clusterrebalancingrequest example-1
    ```

* Apply the second cluster rebalancing request:

    ```sh
    kubectl apply -f rebalancing-req-2.yaml
    ```

* Check the status of the second cluster rebalancing request:

    ```sh
    kubectl get clusterrebalancingrequest example-2 -o yaml
    ```

* After the second request is completed, commit the changes:

    ```sh
    kubectl delete clusterrebalancingrequest example-2
    ```