# Running the Launchpad demo on OpenChoreo

This is the exact sequence used to run [OpenChoreo](https://openchoreo.dev) (the open-source
internal developer platform from Choreo Labs, formerly WSO2) on a laptop and deploy the
Launchpad demo image on it as a Component. It follows the official
[Run Locally on K3d](https://openchoreo.dev/docs/getting-started/try-it-out/on-k3d-locally/)
guide (v1.3.3), control plane and data plane only. If that guide has changed since, follow it
over this page.

Everything below ran on Windows 11 with WSL2 and Docker Desktop, with 10 GB of memory given to
WSL (the guide asks for Docker with 8 GB and 4 CPUs).

## Prerequisites

| Tool | Used here | Needed |
|---|---|---|
| Docker | Docker Desktop, 9.7 GiB available | v26+, 8 GB RAM, 4 CPU |
| k3d | v5.9.0 | v5.8+ |
| kubectl | v1.37.1 | v1.34+ |
| Helm | v3.22.1 | v3.12+ |

## 1. Cluster

```bash
curl -fsSL https://raw.githubusercontent.com/openchoreo/openchoreo/release-v1.3/install/k3d/single-cluster/config.yaml | k3d cluster create --config=-
```

## 2. Prerequisites inside the cluster

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml

helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager --namespace cert-manager --create-namespace --version v1.19.4 --set crds.enabled=true --wait --timeout 180s

helm upgrade --install external-secrets oci://ghcr.io/external-secrets/charts/external-secrets --namespace external-secrets --create-namespace --version 2.0.1 --set installCRDs=true --wait --timeout 180s

helm upgrade --install kgateway-crds oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds --create-namespace --namespace openchoreo-control-plane --version v2.3.11
helm upgrade --install kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway --namespace openchoreo-control-plane --create-namespace --version v2.3.11

helm upgrade --install openbao oci://ghcr.io/openbao/charts/openbao --namespace openbao --create-namespace --version 0.25.6 --values https://raw.githubusercontent.com/openchoreo/openchoreo/release-v1.3/install/k3d/common/values-openbao.yaml --wait --timeout 300s
```

Then the `ClusterSecretStore` (it lets External Secrets read from OpenBao) and the CoreDNS rewrite:

```bash
kubectl apply -f - <<YAML
apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-secrets-openbao
  namespace: openbao
---
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: default
spec:
  provider:
    vault:
      server: "http://openbao.openbao.svc:8200"
      path: "secret"
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "openchoreo-secret-writer-role"
          serviceAccountRef:
            name: "external-secrets-openbao"
            namespace: "openbao"
YAML

kubectl apply -f https://raw.githubusercontent.com/openchoreo/openchoreo/release-v1.3/install/k3d/common/coredns-custom.yaml
```

Check: `kubectl get clustersecretstore default` shows `Valid` and `READY True`.

## 3. Control plane

```bash
helm upgrade --install thunder oci://ghcr.io/thunder-id/helm-charts/thunderid --namespace thunder --create-namespace --version 1.0.1 --values https://raw.githubusercontent.com/openchoreo/openchoreo/release-v1.3/install/k3d/common/values-thunder.yaml
kubectl wait -n thunder --for=condition=available --timeout=300s deployment -l app.kubernetes.io/name=thunderid
```

```bash
kubectl apply -f - <<YAML
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: backstage-secrets
  namespace: openchoreo-control-plane
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: default
  target:
    name: backstage-secrets
  data:
  - secretKey: backend-secret
    remoteRef:
      key: backstage-backend-secret
      property: value
  - secretKey: client-secret
    remoteRef:
      key: backstage-client-secret
      property: value
  - secretKey: jenkins-api-key
    remoteRef:
      key: backstage-jenkins-api-key
      property: value
YAML

helm upgrade --install openchoreo-control-plane oci://ghcr.io/openchoreo/helm-charts/openchoreo-control-plane --version 1.3.3 --namespace openchoreo-control-plane --create-namespace --values https://raw.githubusercontent.com/openchoreo/openchoreo/release-v1.3/install/k3d/single-cluster/values-cp.yaml

kubectl wait -n openchoreo-control-plane --for=condition=available --timeout=300s deployment --all
```

## 4. Default resources

```bash
kubectl label namespace default openchoreo.dev/control-plane=true --overwrite && kubectl apply -f https://raw.githubusercontent.com/openchoreo/openchoreo/release-v1.3/samples/getting-started/all.yaml
```

This creates the `default` Project, the `development`, `staging` and `production`
Environments, a DeploymentPipeline, and the built-in component types (`service`, `worker`,
`web-application`, `scheduled-task`).

## 5. Data plane

```bash
kubectl create namespace openchoreo-data-plane --dry-run=client -o yaml | kubectl apply -f -
kubectl wait -n openchoreo-control-plane --for=condition=Ready certificate/cluster-gateway-ca --timeout=120s
CA_CRT=$(kubectl get secret cluster-gateway-ca -n openchoreo-control-plane -o jsonpath='{.data.ca\.crt}' | base64 -d) && kubectl create configmap cluster-gateway-ca --from-literal=ca.crt="$CA_CRT" -n openchoreo-data-plane --dry-run=client -o yaml | kubectl apply -f -

helm upgrade --install openchoreo-data-plane oci://ghcr.io/openchoreo/helm-charts/openchoreo-data-plane --version 1.3.3 --namespace openchoreo-data-plane --create-namespace --values https://raw.githubusercontent.com/openchoreo/openchoreo/release-v1.3/install/k3d/single-cluster/values-dp.yaml

kubectl wait -n openchoreo-data-plane --for=condition=Ready certificate/cluster-agent-dataplane-tls --timeout=120s
```

Register the data plane with the control plane:

```bash
AGENT_CA=$(kubectl get secret cluster-agent-tls -n openchoreo-data-plane -o jsonpath='{.data.ca\.crt}' | base64 -d)

kubectl apply -f - <<YAML
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterDataPlane
metadata:
  name: default
spec:
  planeID: default
  clusterAgent:
    clientCA:
      value: |
$(echo "$AGENT_CA" | sed 's/^/        /')
  secretStoreRef:
    name: default
  gateway:
    ingress:
      external:
        http:
          host: openchoreoapis.localhost
          listenerName: http
          port: 19080
        name: gateway-default
        namespace: openchoreo-data-plane
YAML
```

The console at `http://openchoreo.localhost:8080` then lists the data plane as **Connected**
(default local login from the official guide: `admin@openchoreo.dev` / `Admin@123`).

## 6. Deploy the Launchpad demo

[`deploy/openchoreo/launchpad-demo.yaml`](../deploy/openchoreo/launchpad-demo.yaml) holds a
`Component` and a `Workload`:

```bash
kubectl apply -f deploy/openchoreo/launchpad-demo.yaml
kubectl wait --for=condition=Ready component/launchpad-demo -n default --timeout=120s
kubectl get deployment,svc,httproute -A -l openchoreo.dev/component=launchpad-demo
```

Result on the laptop:

```text
NAME             PROJECT   COMPONENTTYPE        AGE
launchpad-demo   default   deployment/service   108s

NAMESPACE                                 NAME                                READY   UP-TO-DATE   AVAILABLE
dp-default-default-development-f8e58905   deployment.apps/launchpad-demo-...  1/1     1            1
dp-default-default-development-f8e58905   service/launchpad-demo              ClusterIP   8080/TCP
dp-default-default-development-f8e58905   httproute.../launchpad-demo-http-.. ["development-default.openchoreoapis.localhost"]
```

The route's path prefix is `/launchpad-demo-http`, stripped before it reaches the app:

```bash
curl -i http://development-default.openchoreoapis.localhost:19080/launchpad-demo-http/
# HTTP/1.1 200 OK  (the Launchpad preview page)
```

## Clean up

```bash
k3d cluster stop openchoreo     # keep it, free the memory
k3d cluster delete openchoreo   # remove everything
```
