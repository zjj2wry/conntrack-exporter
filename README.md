# conntrack-exporter
collect conntrack metrics in kubernetes cluster

## install

```bash
kubectl apply -f https://raw.githubusercontent.com/zjj2wry/conntrack-exporter/master/deploy.yaml
```

## grafana

load kubernetes-conntracker-grafana.json in your grafana

![conntrack-grafana](./grafana.jpg)

## TODO
use informer sync kubernetes endpoint cache, convert ip to pod name. reason:
1. If the pod is gone, the ip is gone, and historical data is not helpful
2. podname (host) is more readable