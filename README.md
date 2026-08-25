# ampulla

> An ampulla is a small sealed vessel: you put something in it that you intend to
> still have later.

`ampulla` backs up the services of the [helmetica](https://github.com/helmetica-framework)
framework. A service asks to be backed up by having a `BackupPolicy` next to it, and
ampulla provisions the storage and the schedule that fill it.

This is the scaffold: the API, the controller manager and the deployment manifests. The
provisioning itself — the COSI bucket, the credentials and the k8up schedule — lands on top
of it.

## Asking for backups

The policy is the entire contract. It carries no reference to whatever created it:

```yaml
apiVersion: backups.helmetica.io/v1
kind: BackupPolicy
metadata:
  name: orders
spec: {}
```

An empty spec is a complete policy — it takes the controller's defaults. Each field
overrides one of them:

| Field | Meaning |
| --- | --- |
| `mode` | `Schedule`: ampulla drives k8up. `BucketOnly`: ampulla provisions the bucket and the service backs itself up into it |
| `schedule` | When the backup runs. Mode `Schedule` only |
| `pruneSchedule` | When old snapshots are forgotten and pruned. Mode `Schedule` only |
| `checkSchedule` | When the backup repository is verified. Mode `Schedule` only |
| `retention.keep{Last,Hourly,Daily,Weekly,Monthly,Yearly}` | How many snapshots survive a prune. Mode `Schedule` only |
| `bucketClassName` | Which object storage the backups land in |
| `bucketAccessClassName` | Which COSI BucketAccessClass mints the credentials |

The result is reported back on the policy:

```console
$ kubectl get backuppolicies
NAME     MODE       PHASE     BUCKET   MESSAGE
orders   Schedule   Pending            nothing provisioned yet
```

## Where the policy comes from

Nothing in ampulla creates policies. In the framework a reagent's chart renders one from
its own values — the [ferment](https://github.com/helmetica-framework/ferment) starter
chart every reagent is scaffolded from ships the template and the values block, so a
service's owner writes `backup.enabled: true` and the chart does the rest. Helm puts the
policy in the release namespace, which is where the service and its volumes already are.

That chart is the only thing ampulla and the rest of the framework have in common. ampulla
has no dependency on the controller that generated the service's API, does not watch its
resources, and cannot tell whether one exists.

Applying a policy by hand works just as well, which is what the tests do.

## Running it

```console
just build        # build the binary
just test         # unit tests
just build-docker # container image
just run          # run against the cluster in your current kubeconfig
```

`just --list` shows every recipe.

## Trying it on the athanor kind cluster

From the athanor devcontainer, with the cluster up (`just ignite`):

```console
just deploy-kind
kubectl apply -f config/samples/backuppolicy.yaml
kubectl get backuppolicies
kubectl -n ampulla-system logs deployment/ampulla-controller-manager -f
```

This builds the image, `kind load`s it into the `athanor` cluster, applies the CRD and
rolls out the deployment. Re-run it after every change.
