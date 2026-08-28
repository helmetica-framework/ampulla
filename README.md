# ampulla

> An ampulla is a small sealed vessel: you put something in it that you intend to
> still have later.

`ampulla` backs up the services of the [helmetica](https://github.com/helmetica-framework)
framework. A service asks to be backed up by having a `BackupPolicy` next to it, and
ampulla provisions the storage and the schedule that fill it:

```
BackupPolicy (in the service's namespace)
        │
        ├─► BucketClaim ──► (COSI driver) ──► a bucket
        ├─► BucketAccess ─► (COSI driver) ──► Secret: endpoint, bucket id, key pair
        ├─► Secret ────────────────────────────────────────► restic repository password
        └─► k8up Schedule ─► backs up every PVC in the namespace,
                             reading the key pair out of COSI's Secret
```

Everything ampulla creates is owned by the policy and lives beside it, so a deleted policy
— or a deleted namespace — takes the lot with it through ordinary garbage collection.

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

| Field | Default | Meaning |
| --- | --- | --- |
| `mode` | `Schedule` | `Schedule`: ampulla drives k8up. `BucketOnly`: ampulla provisions the bucket and the service backs itself up into it |
| `schedule.backup` | `--default-schedule` (`@daily-random`) | When the backup runs |
| `schedule.prune` | `--default-prune-schedule` (`@weekly-random`) | When old snapshots are forgotten and pruned. Empty means no prune job |
| `schedule.check` | `--default-check-schedule` (`@weekly-random`) | When the restic repository is verified. Empty means no check job |
| `retention.keep{Last,Hourly,Daily,Weekly,Monthly,Yearly}` | `keepDaily: 7`, `keepWeekly: 4`, `keepMonthly: 6` | How many snapshots survive a prune |
| `bucketClassName` | `--default-bucket-class` | Which object storage the backups land in |
| `bucketAccessClassName` | `--default-bucket-access-class` | Which COSI BucketAccessClass mints the credentials |

The `-random` schedules are k8up's: they spread the jobs of every service in the cluster
over the window instead of starting every backup in the same minute.

The result is reported back on the policy:

```console
$ kubectl get backuppolicy orders -o jsonpath='{.status}' | jq
{
  "phase": "Ready",
  "observedGeneration": 1,
  "bucket": "bucket-8f2a1c...",
  "endpoint": "http://garage-s3.garage-system.svc:3900",
  "credentialsSecret": "orders-backup-credentials",
  "schedule": "0 2 * * *",
  "message": "Backing up to bucket-8f2a1c... in http://garage-s3.garage-system.svc:3900"
}
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

## Services that back themselves up

Some services already know how to write backups to object storage: CloudNativePG has
`Backup`/`ScheduledBackup`, mariadb-operator has `PhysicalBackup`. Backing their volumes up
with k8up on top of that is redundant at best, and at worst it is an inconsistent copy of a
running database taken next to a consistent one.

`mode: BucketOnly` provisions the bucket and its credentials and stops — no `Schedule`, no
restic repository password, and `schedule`/`retention` are ignored. What the service needs
is in `.status`: `bucket`, `endpoint` and `credentialsSecret`. That Secret holds one key
per value (`COSI_S3_ACCESS_KEY_ID`, `COSI_S3_ACCESS_SECRET_KEY`, …), which is the shape
both of those operators want for their S3 credentials, so their CRs reference it directly
and no credential is copied anywhere. Note that most operators want the endpoint as
`host:port` plus a separate TLS flag, while COSI publishes a URL; the scheme has to be
stripped.

Switching a policy from `Schedule` to `BucketOnly` deletes the k8up Schedule and keeps the
bucket and the repository password, so switching back does not orphan the snapshots
already in there.

## What it needs in the cluster

* **COSI** (`objectstorage.k8s.io/v1alpha2`) — the CRDs, the central controller, and a
  driver, plus a `BucketClass` and a `BucketAccessClass` to provision from. athanor ships
  all of it: the garage operator's driver backed by a single-node Garage cluster.
* **k8up**, which runs the Schedules. Not needed for `BucketOnly` policies.

Nothing else is required of the cluster. ampulla's CRD carries ClusterRoles that aggregate
into the built-in `admin`, `edit` and `view` roles, so whoever is allowed to deploy a
service into a namespace is allowed to manage the BackupPolicy its chart renders there —
Kubernetes hides custom resources from those roles unless a controller opts them in.

There is no default bucket class. Which object storage a customer's data ends up in is not
a decision to guess at, so a policy without a class named either on itself or on the
controller is rejected — the policy goes to phase `Failed` with the reason in `.status.message` — rather than backed up
somewhere arbitrary.

## Design notes

**ampulla never handles the credentials.** COSI v1alpha2 writes the bucket info and the key
pair into one Secret, a key per value, which is the shape k8up's secret references want.
The Schedule points straight at that Secret, so the key pair goes from COSI to the backup
job without being copied anywhere. ampulla reads the Secret only for the endpoint and the
bucket id, which it needs to fill in the backend.

**The repository password is created once and never rewritten.** It is generated on the
first reconcile and then left alone: overwriting it would leave every snapshot already in
the bucket unreadable.

**What becomes of a deleted bucket is the driver's business.** Deleting a policy deletes
its BucketClaim, and from there the `BucketClass` deletion policy decides: `Retain` keeps
the bucket, `Delete` hands it to the driver. Whether a driver will delete a bucket that
still holds objects is up to the driver — `aludel-cloudscale` takes a
`bucketDeletionPolicy: DeleteAll` parameter that empties it first; the garage operator
refuses a non-empty bucket outright, so use `Retain` with it until it grows the same
option. ampulla deliberately does not empty buckets itself: it only ever holds a borrowed
credential that the driver revokes the moment a `BucketAccess` is marked for deletion.

**One policy, one bucket, one repository.** No sharing, no prefixes, nothing to keep apart
inside a bucket — which is why the retention policy carries no tag or hostname filters.

## Running it

```console
just build        # build the binary
just test         # unit tests and envtest integration tests
just build-docker # container image
just run          # run against the cluster in your current kubeconfig
```

`just --list` shows every recipe. `just test` needs no third-party CRDs checked into this
repo: COSI's and k8up's are read out of the module cache of the two Go dependencies that
already ship them, and ampulla's own comes from `config/crd`.

## Trying it on the athanor kind cluster

From the athanor devcontainer, with the cluster up (`just ignite`). k8up, COSI and the
garage operator's driver are all part of `just ignite`, and nothing here talks to a cloud
provider: the buckets are real buckets in the Garage cluster running next door.

**1. Build and side-load ampulla:**

```console
just deploy-kind
```

This builds the image, `kind load`s it into the `athanor` cluster, applies the CRD and
rolls out the deployment. Re-run it after every change. The `config/dev` overlay defaults
the controller to athanor's `garage` classes, so a policy only has to exist.

**2. Ask for backups** — either by setting `backup.enabled: true` on a service whose chart
renders a policy, or by applying one directly:

```console
kubectl apply -f config/samples/backuppolicy.yaml
kubectl get backuppolicies
kubectl -n ampulla-system logs deployment/ampulla-controller-manager -f
```

```console
NAME     MODE       PHASE   BUCKET             MESSAGE
orders   Schedule   Ready   bucket-8f2a1c...   backing up to bucket-8f2a1c... in http://…
```

Everything else lands in the same namespace as the policy: `kubectl get
bucketclaims,bucketaccesses,schedules`. Once k8up has run, the snapshots show up as
`kubectl get snapshots`.

## Status

Pre-alpha, and so is COSI. This targets COSI **v1alpha2**, which upstream serves only from
`main` — the dependency is pinned to the same revision the garage operator builds against,
so athanor's cluster and this controller speak the same API — and k8up v2.
