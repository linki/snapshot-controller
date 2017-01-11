package main

import (
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"gopkg.in/alecthomas/kingpin.v2"

	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"

	"google.golang.org/api/compute/v0.beta"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"
)

const (
	maxSnapshotNameLength = 63
	randomSuffixLength    = 4
	operationPollDelay    = 2 * time.Second
	statusDone            = "DONE"
	defaultInterval       = "1h"
	defaultRetain         = "48"
	heritageLabel         = "snapshot-controller"
)

var (
	project   string
	zone      string
	interval  time.Duration
	retain    int
	inCluster bool
	once      bool
	dryRun    bool
	debug     bool
	version   = "unknown"
)

type SnapshotConfiguration struct {
	Project        string
	Zone           string
	SourceDiskName string
	SnapshotName   string
	Labels         map[string]string
}

type newestFirst []compute.Snapshot

func (ss newestFirst) Len() int           { return len(ss) }
func (ss newestFirst) Swap(i, j int)      { ss[i], ss[j] = ss[j], ss[i] }
func (ss newestFirst) Less(i, j int) bool { return ss[i].CreationTimestamp > ss[j].CreationTimestamp }

func init() {
	kingpin.Flag("project", "The ID of GCP project.").Required().StringVar(&project)
	kingpin.Flag("zone", "The name of the zone the disks live in.").Required().StringVar(&zone)
	kingpin.Flag("interval", "Interval between Pod terminations").Default(defaultInterval).DurationVar(&interval)
	kingpin.Flag("retain", "The maximum number of snapshots to retain per disk.").Default(defaultRetain).IntVar(&retain)
	kingpin.Flag("in-cluster", "If true, finds the Kubernetes cluster from the environment").BoolVar(&inCluster)
	kingpin.Flag("once", "Run once and exit").BoolVar(&once)
	kingpin.Flag("dry-run", "If true, don't create any snapshots.").BoolVar(&dryRun)
	kingpin.Flag("debug", "Enable debug logging").BoolVar(&debug)

	rand.Seed(time.Now().UnixNano())
}

func main() {
	kingpin.Version(version)
	kingpin.Parse()

	if debug {
		log.SetLevel(log.DebugLevel)
	}

	kube, err := newKubernetesClient()
	if err != nil {
		log.Fatal(err)
	}

	gce, err := newGCEComputeClient()
	if err != nil {
		log.Fatal(err)
	}

	for {
		pvl, err := kube.Core().PersistentVolumes().List(v1.ListOptions{})
		if err != nil {
			log.Fatal(err)
		}

		pvl = filterKubernetesPersistentVolumesByGCEPersistentDisks(pvl)

		log.Info("Found the following Kubernetes Persistent Volumes:")

		for _, pv := range pvl.Items {
			log.Infof("  %s", pv.Spec.GCEPersistentDisk.PDName)
		}

		dl, err := gce.Disks.List(project, zone).Do()
		if err != nil {
			log.Fatal(err)
		}

		dl = filterGCEPersistentDisksByKubernetesPersistentVolumes(dl, pvl)

		log.Info("Found the following Google Cloud Engine Persistent Disks:")

		for _, d := range dl.Items {
			log.Infof("  %s", d.Name)
		}

		sl, err := gce.Snapshots.List(project).Do()
		if err != nil {
			log.Fatal(err)
		}

		sl = filterGCESnapshotsByGCEPersistentDisks(sl, dl)

		log.Info("Found the following Google Cloud Engine Snapshots:")

		for _, s := range sl.Items {
			sourceDisk, _ := url.Parse(s.SourceDisk)
			parts := strings.Split(sourceDisk.String(), "/")
			sourceDiskName := parts[len(parts)-1]

			log.Infof("  %s (for: %s)", s.Name, sourceDiskName)
		}

		scs := calculateSnapshotConfiguration(dl, sl, pvl)

		log.Info("Going to create the following snapshots:")

		for _, sc := range scs {
			log.Infof("  %s -> %s", sc.SourceDiskName, sc.SnapshotName)
		}

		ess := calculateExpiredSnapshots(sl)

		log.Info("Going to delete the following expired snapshots:")

		for _, s := range ess {
			log.Infof("  %s", s.Name)
		}

		err = createSnapshots(scs, gce, dryRun)
		if err != nil {
			log.Fatal(err)
		}

		err = deleteSnapshots(ess, gce, dryRun)
		if err != nil {
			log.Fatal(err)
		}

		if once {
			log.Infof("Supposed to run once. Exiting...")
			break
		}

		log.Infof("Sleeping for %s...", interval)
		time.Sleep(interval)
	}
}

func filterKubernetesPersistentVolumesByGCEPersistentDisks(pvl *v1.PersistentVolumeList) *v1.PersistentVolumeList {
	fl := &v1.PersistentVolumeList{}

	for _, pv := range pvl.Items {
		if pv.Spec.GCEPersistentDisk != nil {
			fl.Items = append(fl.Items, pv)
		}
	}

	return fl
}

func filterGCEPersistentDisksByKubernetesPersistentVolumes(dl *compute.DiskList, pvl *v1.PersistentVolumeList) *compute.DiskList {
	fl := &compute.DiskList{}

	for _, d := range dl.Items {
		for _, pv := range pvl.Items {
			if d.Name == pv.Spec.GCEPersistentDisk.PDName {
				fl.Items = append(fl.Items, d)
			}
		}
	}

	return fl
}

func filterGCESnapshotsByGCEPersistentDisks(sl *compute.SnapshotList, dl *compute.DiskList) *compute.SnapshotList {
	fl := &compute.SnapshotList{}

	for _, s := range sl.Items {
		for _, d := range dl.Items {
			if s.SourceDisk == d.SelfLink {
				fl.Items = append(fl.Items, s)
			}
		}
	}

	return fl
}

func calculateSnapshotConfiguration(dl *compute.DiskList, _ *compute.SnapshotList, pvl *v1.PersistentVolumeList) []SnapshotConfiguration {
	scs := make([]SnapshotConfiguration, 0, len(dl.Items))

	for _, d := range dl.Items {
		sc := SnapshotConfiguration{
			Project:        project,
			Zone:           zone,
			SourceDiskName: d.Name,
			SnapshotName:   calculateSnapshotName(d.Name),
			Labels:         calculateSnapshotLabels(d.Name, pvl),
		}
		scs = append(scs, sc)
	}

	return scs
}

func calculateSnapshotName(diskName string) string {
	snapshotNameCutoffLength := maxSnapshotNameLength - randomSuffixLength - 1

	if len(diskName) > snapshotNameCutoffLength {
		diskName = diskName[:snapshotNameCutoffLength]
	}

	return diskName + "-" + randomString(randomSuffixLength)
}

func calculateSnapshotLabels(diskName string, pvl *v1.PersistentVolumeList) map[string]string {
	labels := make(map[string]string)

	labels["heritage"] = heritageLabel

	for _, pv := range pvl.Items {
		if diskName == pv.Spec.GCEPersistentDisk.PDName {
			labels["persistentvolume"] = pv.Name
		}
	}

	return labels
}

func createSnapshots(scs []SnapshotConfiguration, gce *compute.Service, dryRun bool) error {
	if dryRun {
		log.Info("Dry run enabled. Skipping real snapshot creation.")
		return nil
	}

	for _, sc := range scs {
		s := &compute.Snapshot{
			Name: sc.SnapshotName,
		}

		op, err := gce.Disks.CreateSnapshot(sc.Project, sc.Zone, sc.SourceDiskName, s).Do()
		if err != nil {
			return err
		}

		for op.Status != statusDone {
			time.Sleep(operationPollDelay)

			op, err = gce.ZoneOperations.Get(sc.Project, sc.Zone, op.Name).Do()
			if err != nil {
				return err
			}

			log.Debug(op.Status)
		}

		s, err = gce.Snapshots.Get(sc.Project, sc.SnapshotName).Do()
		if err != nil {
			return err
		}

		setLabelsParams := &compute.GlobalSetLabelsRequest{
			LabelFingerprint: s.LabelFingerprint,
			Labels:           sc.Labels,
		}

		op, err = gce.Snapshots.SetLabels(sc.Project, sc.SnapshotName, setLabelsParams).Do()
		if err != nil {
			return err
		}

		for op.Status != statusDone {
			time.Sleep(operationPollDelay)

			op, err = gce.GlobalOperations.Get(sc.Project, op.Name).Do()
			if err != nil {
				return err
			}

			log.Debug(op.Status)
		}
	}

	return nil
}

func calculateExpiredSnapshots(sl *compute.SnapshotList) []compute.Snapshot {
	sm := make(map[string][]compute.Snapshot)

	for _, s := range sl.Items {
		if s.Labels["heritage"] != heritageLabel {
			continue
		}

		sm[s.SourceDisk] = append(sm[s.SourceDisk], *s)
	}

	for _, ss := range sm {
		sort.Sort(newestFirst(ss))
	}

	log.Debugf("Snapshots grouped by source disk and ordered by creation time:")

	for sn, ss := range sm {
		sourceDisk, _ := url.Parse(sn)
		parts := strings.Split(sourceDisk.String(), "/")
		sourceDiskName := parts[len(parts)-1]

		log.Debugf("%s", sourceDiskName)

		for _, s := range ss {
			log.Debugf("  %s (%s)", s.Name, s.CreationTimestamp)
		}
	}

	rss := make([]compute.Snapshot, 0)

	for sn := range sm {
		if len(sm[sn]) > retain {
			rss = append(rss, sm[sn][retain:len(sm[sn])]...)
		}
	}

	return rss
}

func deleteSnapshots(scs []compute.Snapshot, gce *compute.Service, dryRun bool) error {
	if dryRun {
		log.Info("Dry run enabled. Skipping real snapshot deletion.")
		return nil
	}

	for _, s := range scs {
		op, err := gce.Snapshots.Delete(project, s.Name).Do()
		if err != nil {
			return err
		}

		for op.Status != statusDone {
			time.Sleep(operationPollDelay)

			op, err = gce.GlobalOperations.Get(project, op.Name).Do()
			if err != nil {
				return err
			}

			log.Debug(op.Status)
		}
	}

	return nil
}

func newDefaultClient(scopes ...string) (*http.Client, error) {
	return google.DefaultClient(context.Background(), scopes...)
}

func newGCEComputeClient() (*compute.Service, error) {
	gc, err := newDefaultClient(compute.ComputeScope)
	if err != nil {
		return nil, err
	}

	return compute.New(gc)
}

func newKubernetesClient() (*kubernetes.Clientset, error) {
	var (
		config *rest.Config
		err    error
	)

	if inCluster {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
	} else {
		config = &rest.Config{
			Host: "http://127.0.0.1:8001",
		}
	}

	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return kube, nil
}

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyz0123456789")

func randomString(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}
