# MCP Gateway Deployment on OpenShift

The MCP Gateway can be deployed to an OpenShift environment. The assets in this directory cover the main OpenShift deployment flows:

- OpenShift Service Mesh or OpenShift Cluster Ingress-backed Gateway API
- MCP Gateway controller and gateway instance
- OpenShift Route exposure for the gateway
- Optional Red Hat Connectivity Link installation

## Deploying to OpenShift

A script named [deploy_openshift.sh](deploy_openshift.sh) is available to facilitate the deployment to OpenShift.

By default the script:

- installs Service Mesh support unless `INSTALL_SERVICE_MESH=false`
- skips Red Hat Connectivity Link unless `INSTALL_RHCL=true`
- installs the MCP Gateway controller through OLM
- deploys the gateway instance and an OpenShift Route

Execute the following command to deploy:

```shell
./deploy_openshift.sh
```

Useful environment variables:

- `MCP_GATEWAY_HOST` to override the public hostname
- `MCP_GATEWAY_NAMESPACE` to change the MCP Gateway namespace
- `GATEWAY_NAMESPACE` to change the Gateway namespace
- `INSTALL_RHCL=true` to install Red Hat Connectivity Link explicitly
- `USE_OCP_INGRESS=false` to use the Istio GatewayClass path instead of `openshift-default`

The MCP Gateway will be available at the output of the following command:

```shell
echo https://$(oc get routes -n gateway-system -o jsonpath='{ .items[0].spec.host }')/mcp
```

## Deploying to OpenShift using OpenShift GitOps (Argo CD)

OpenShift GitOps (Argo CD) can be used to deploy the MCP Gateway to an OpenShift environment.

### Prerequisites

1. Cluster scoped OpenShift GitOps previously deployed

### Deployment

Execute the following command to deploy the MCP Gateway to OpenShift using OpenShift GitOps


```shell
./deploy_openshift_argocd.sh
```

The MCP Gateway will be available at the output of the following command:

```shell
echo https://$(oc get routes -n gateway-system -o jsonpath='{ .items[0].spec.host }')/mcp
```

## Verify Access to the MCP Gateway

To confirm that MCP Gateway has been deployed successfully, execute the following command:

```shell
curl -k -LX POST https://$(oc get routes -n gateway-system -o jsonpath='{ .items[0].spec.host }')/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"verify","version":"1.0"}}}'
```

A response similar to the following indicates the MCP Gateway was successfully deployed


```shell
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"Kuadrant MCP Gateway","version":"0.0.1"}}}
```
