kubectl annotate deploy busybox kubefleet.dev/cluster-selectors-from-tenant="region=abc"
kubectl annotate job busybox kubefleet.dev/cluster-selectors-from-tenant="region=abc"