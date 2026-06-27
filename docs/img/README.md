# Diagrams

Rendered flow images (the source `.mmd` files render live on GitHub; the `.png`
versions are for embedding elsewhere, e.g. chat).

## Deploy flow

How `deployment-deploy` takes a container image live on Jetder, then `deployment-get`
returns the URL.

![Deploy flow](./deploy-flow.png)

Source: [`deploy-flow.mmd`](./deploy-flow.mmd).

## Domain → route flow

How a custom domain is added, its DNS records are set in Cloudflare (proxied for
instant SSL), and `route-create-v2` points it at a deployment.

![Domain to route flow](./domain-route-flow.png)

Source: [`domain-route-flow.mmd`](./domain-route-flow.mmd). See
[ARCHITECTURE.md](../ARCHITECTURE.md) for the full set of diagrams.
