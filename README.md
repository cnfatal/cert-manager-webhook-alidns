# ACME webhook for Alibaba Cloud DNS

The ACME issuer type supports an optional 'webhook' solver, which can be used
for Alibaba Cloud DNS.

more details: https://cert-manager.io/docs/configuration/acme/dns01/webhook/

## Usage

Install webhook from allinone bundle or using helm chart under [deploy/cert-manager-webhook-alidns](deploy/cert-manager-webhook-alidns).

```sh
# install cert-manager webhook
kubectl apply -f https://raw.githubusercontent.com/fatalc/cert-manager-webhook-alidns/main/deploy/rendered-manifest.yaml
```

[Obtain an AccessKey pair](https://www.alibabacloud.com/help/en/doc-detail/107708.htm) and create the AccessKey Secret.

```sh
# create alidns aksk secret
kubectl -n cert-manager create secret generic alidns-secret --from-literal="access-key=<AccessKey ID>" --from-literal="secret-key=<AccessKey Secret>"
```

Create the ACME issuer. for more information see <https://cert-manager.io/docs/configuration/acme/>

```sh
cat <<EOF | kubectl create --edit -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    # You must replace this email address with your own.
    # Let's Encrypt will use this to contact you about expiring
    # certificates, and issues related to your account.
    email: contact@example.com
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      # Secret resource that will be used to store the account's private key.
      name: letsencrypt-issuer-account-key
    solvers:
    - dns01:
        webhook:
            groupName: dns.aliyun.com
            solverName: alidns-solver
            config:
              regionId: ""                 # optional
              apiKeySecretRef:
                name: alidns-secret
EOF
```

> Note: The [acme-staging-v02](https://letsencrypt.org/docs/staging-environment/#) api: <https://acme-staging-v02.api.letsencrypt.org/directory> is only for testing purposes now.

or you can set AccsessKey in webhook configuration directly (**use as your own risk**):

```diff
-              apiKeySecretRef:
-                name: alidns-secret
+              accessKeyID: "<accessKeyID>"
+              accessKeySecret: "<accessKeySecret>"
```

Issue a certificate(optional)

```sh
cat <<EOF | kubectl create --edit -f -
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: example-tls
spec:
  secretName: example-com-tls
  commonName: example.com
  dnsNames:
  - example.com
  - "*.example.com"
  issuerRef:
    name: letsencrypt
    kind: ClusterIssuer
EOF
```

## Build

required: `golang 1.17` `buildah` `helm`

```sh
make build
make rendered-manifest.yaml
```

## Running the test suite

update [alidns-secret](testdata/alidns-solver/alidns-secret.yaml) to your own secret

```bash
$ TEST_ZONE_NAME=example.com. make test
```
