# deputy

`Deputy` snapshots the GCE Persistent Disks that are backing your Kubernetes
Persistent Volumes.

# Purpose

Kubernetes has the concept of Persistent Volumes which is an abstraction over
some form of persistent storage in the environment that Kubernetes runs in. Two
of the most popular implementations of Persistent Volumes are GCE Persistent
Disks and AWS EBS volumes. Both of them have the ability to be snapshotted while
they are attached to a node.

`Deputy` is a controller in your cluster that finds any Persistent Volumes
backed by GCE Persistent Disks and regularly takes a snapshot of them.

# Usage

```
$ go get -u github.com/linki/deputy
$ kubectl proxy &
$ deputy --project=some-project --zone=europe-west1-d --interval=1h
INFO[0000] Found the following Kubernetes Persistent Volumes:
INFO[0000]   gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8400d7
...
INFO[0001] Found the following Google Cloud Engine Persistent Disks:
INFO[0001]   gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8400d7
...
INFO[0002] Found the following Google Cloud Engine Snapshots:
INFO[0002]   gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8-1b4m (for: gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8400d7)
INFO[0002]   gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8-1wus (for: gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8400d7)
...
INFO[0002] Going to create the following snapshots:
INFO[0002]   gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8400d7 -> gke-somename-29c0222a--pvc-01d45471-c249-11e6-a767-42010a8-onjf
...
INFO[0050] Sleeping for 1h0m0s...
```

If you're running inside the same Kubernetes cluster as the PVs you want to
snapshot, you can use the `--in-cluster` flag instead of using `kubectl proxy`.

See [deputy.yml](deputy.yml) for an example Deployment manifest.

# Caveats

This is a very early state and there are many things that need improvement:

* Limited to a GKE cluster (a custom deployed cluster on GCE should also work)
* Limited to a single zone (disks in other zones won't be found)
* Runs, takes snapshots, sleeps. It will do this on every restart of the
  process. It also doesn't clean up old snapshots.
* Takes a snapshots at any time, regardless how your disk is used at that time.

# Related Work

[k8s-snapshots](https://github.com/miracle2k/k8s-snapshots/tree/v0.1) by
[miracle2k](https://github.com/miracle2k) is a similar project implemented in
Python and started about the same time as this project. It looks for annotations
on Persistent Volumes and snapshots them. It supports a quite sophisticated
backup retention policy and also removes old snapshots. Limited to GCE
Persistent Disks as is this tool.

[volume-snapshotting.md](https://github.com/kubernetes/kubernetes/blob/v1.5.1/docs/design/volume-snapshotting.md)
by [Cindy Wang](https://github.com/ciwang) is a design doc for standardizing
volume backups and restores for a Kubernetes cluster. It's based on the same
idea of using the cloud provider's disk snapshot abilities and goes into much
more detail on how this can be done in a safe way and integrated into the
Kubernetes API. It also describes how restores from those snapshots can work.
Something that this tool doesn't even consider.
