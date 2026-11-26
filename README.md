# ampulla

> An ampulla is a small sealed vessel: you put something in it that you intend to still have later.

`ampulla` backs up the services of the helmetica framework. A service asks to be backed up by having a `BackupPolicy` next to it, and ampulla provisions the COSI bucket and the k8up schedule that fill it.

Documentation: https://docs.helmetica.io/ampulla/

## Development

```console
just build        # build the binary
just test         # unit tests and envtest integration tests
just run          # run against the cluster in your current kubeconfig
just docs         # preview the docs
```
