package metahelm

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/dollarshaveclub/metahelm/pkg/dag"
	"github.com/ghodss/yaml"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	batchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/helm/pkg/helm"
	rls "k8s.io/helm/pkg/proto/hapi/services"
	deploymentutil "k8s.io/kubernetes/pkg/controller/deployment/util"
)

// K8sClient describes an object that functions as a Kubernetes client
type K8sClient interface {
	AppsV1() appsv1.AppsV1Interface
	// ExtensionsV1beta1() v1beta1.ExtensionsV1beta1Interface
	CoreV1() corev1.CoreV1Interface
	BatchV1() batchv1.BatchV1Interface
}

// HelmClient describes an object that functions as a Helm client
type HelmClient interface {
	InstallReleaseWithContext(ctx context.Context, chstr, ns string, opts ...helm.InstallOption) (*rls.InstallReleaseResponse, error)
	UpdateReleaseWithContext(ctx context.Context, rlsName string, chstr string, opts ...helm.UpdateOption) (*rls.UpdateReleaseResponse, error)
	ListReleases(opts ...helm.ReleaseListOption) (*rls.ListReleasesResponse, error)
	ReleaseContent(rlsName string, opts ...helm.ContentOption) (*rls.GetReleaseContentResponse, error)
}

// LogFunc is a function that logs a formatted string somewhere
type LogFunc func(string, ...interface{})

// Manager is an object that manages installation of chart graphs
type Manager struct {
	K8c  K8sClient
	HC   HelmClient
	LogF LogFunc
}

func (m *Manager) log(msg string, args ...interface{}) {
	if m.LogF != nil {
		m.LogF(msg, args...)
	}
}

type options struct {
	k8sNamespace, tillerNamespace, releaseNamePrefix string
	installCallback                                  InstallCallback
	timeout                                          time.Duration
}

type InstallOption func(*options)

// WithK8sNamespace specifies the kubernetes namespace to install a chart graph into. DefaultK8sNamespace is used otherwise.
func WithK8sNamespace(ns string) InstallOption {
	return func(op *options) {
		op.k8sNamespace = ns
	}
}

// WithTillerNamespace specifies the namespace where the Tiller service can be found
func WithTillerNamespace(tns string) InstallOption {
	return func(op *options) {
		op.tillerNamespace = tns
	}
}

// WithReleaseNamePrefix specifies a prefix to use in Helm release names (useful for when multiple instances of a chart graph are installed into the same namespace)
func WithReleaseNamePrefix(pfx string) InstallOption {
	return func(op *options) {
		op.releaseNamePrefix = pfx
	}
}

// WithTimeout sets a timeout for all chart installations/upgrades to complete. If the timeout is reached, chart operations are aborted and an error is returned.
func WithTimeout(timeout time.Duration) InstallOption {
	return func(op *options) {
		op.timeout = timeout
	}
}

// WithInstallCallback specifies a callback function that will be invoked immediately prior to each chart installation
func WithInstallCallback(cb InstallCallback) InstallOption {
	return func(op *options) {
		op.installCallback = cb
	}
}

// CallbackAction indicates the decision made by the callback
type InstallCallbackAction int

const (
	// Continue indicates the installation should proceed immediately
	Continue InstallCallbackAction = iota
	// Wait means the install should not happen right now but should be retried at some point in the future. The callback will be invoked again on the retry.
	Wait
	// Abort means the installation should not be attempted
	Abort
)

// InstallCallback is a function that decides whether to proceed with an individual chart installation
// This will be called concurrently from multiple goroutines, so make sure everything is threadsafe
type InstallCallback func(Chart) InstallCallbackAction

// ReleaseMap is a map of chart title to installed release name
type ReleaseMap map[string]string

// release names
type lockingReleases struct {
	sync.Mutex
	rmap ReleaseMap
}

// DefaultK8sNamespace is the k8s namespace to install a chart graph into if not specified
const DefaultK8sNamespace = "default"

var retryDelay = 10 * time.Second

// Install installs charts in order according to dependencies and returns the names of the releases, or error.
// In the event of an error, the client can check if the error returned is of type ChartError, which then provides information on the kubernetes objects
// that caused failure, if this can be determined. A helm error unrelated to pod failure may return either a non-ChartError error value or an empty ChartError.
func (m *Manager) Install(ctx context.Context, charts []Chart, opts ...InstallOption) (ReleaseMap, error) {
	return m.installOrUpgrade(ctx, nil, false, charts, opts...)
}

// Upgrade upgrades charts in order according to dependencies, using the release names in rmap. ValueOverrides will be used in the upgrade.
// In the event of an error, the client can check if the error returned is of type ChartError, which then provides information on the kubernetes objects
// that caused failure, if this can be determined. A helm error unrelated to pod failure may return either a non-ChartError error value or an empty ChartError.
func (m *Manager) Upgrade(ctx context.Context, rmap ReleaseMap, charts []Chart, opts ...InstallOption) error {
	for _, c := range charts {
		if _, ok := rmap[c.Title]; !ok {
			return fmt.Errorf("chart title missing from release map: %v", c.Title)
		}
	}
	_, err := m.installOrUpgrade(ctx, rmap, true, charts, opts...)
	return err
}

// releaseName returns a release name of not more than 53 characters. If the input is truncated, a random number is added to ensure uniqueness.
func ReleaseName(input string) string {
	rsl := []rune(input)
	if len(rsl) < 54 {
		return input
	}
	out := rsl[0 : 53-6]
	rand.Seed(time.Now().UTC().UnixNano())
	return fmt.Sprintf("%v-%d", string(out), rand.Intn(99999))
}

// MaxPodLogLines is the maximum number of failed pod log lines to return in the event of chart install/upgrade failure
var MaxPodLogLines = uint(500)

// installOrUpgrade does helm installs/upgrades in DAG order
func (m *Manager) installOrUpgrade(ctx context.Context, upgradeMap ReleaseMap, upgrade bool, charts []Chart, opts ...InstallOption) (ReleaseMap, error) {
	ops := &options{}
	for _, opt := range opts {
		opt(ops)
	}
	if len(charts) == 0 {
		return nil, errors.New("no charts were supplied")
	}
	if ops.k8sNamespace == "" {
		ops.k8sNamespace = DefaultK8sNamespace
	}
	cmap := map[string]*Chart{}
	objs := []dag.GraphObject{}
	for i := range charts {
		if charts[i].WaitTimeout == 0 {
			charts[i].WaitTimeout = DefaultDeploymentTimeout
		}
		if charts[i].Location == "" {
			return nil, fmt.Errorf("empty location for chart: %v (offset %v)", charts[i].Title, i)
		}
		switch charts[i].DeploymentHealthIndication {
		case IgnorePodHealth:
		case AllPodsHealthy:
		case AtLeastOnePodHealthy:
		default:
			return nil, fmt.Errorf("unknown value for DeploymentHealthIndication: %v", charts[i].DeploymentHealthIndication)
		}
		cmap[charts[i].Name()] = &charts[i]
		objs = append(objs, &charts[i])
	}
	lf := func(msg string, args ...interface{}) {
		if m.LogF != nil {
			m.LogF("objgraph: "+msg, args...)
		}
	}
	og := dag.ObjectGraph{LogF: dag.LogFunc(lf)}
	if err := og.Build(objs); err != nil {
		return nil, errors.Wrap(err, "error building graph")
	}
	rn := lockingReleases{rmap: make(map[string]string)}
	started := time.Now().UTC()
	var deadline time.Time
	if ops.timeout > 0 {
		deadline = started.Add(ops.timeout)
	}
	af := func(obj dag.GraphObject) error {
		m.log("%v: starting install", obj.Name())
	Loop:
		for {
			if ops.installCallback == nil {
				m.log("%v: install callback is not set; proceeding", obj.Name())
				break
			}
			v := ops.installCallback(*cmap[obj.Name()])
			switch v {
			case Continue:
				m.log("%v: install callback indicated Continue; proceeding", obj.Name())
				break Loop
			case Wait:
				m.log("%v: install callback indicated Wait; delaying", obj.Name())
				time.Sleep(retryDelay)
			case Abort:
				m.log("%v: install callback indicated Abort; aborting", obj.Name())
				return errors.New("callback requested abort")
			default:
				return fmt.Errorf("unknown callback result: %v", v)
			}
			if deadline.After(started) && time.Now().UTC().After(deadline) {
				return fmt.Errorf("timeout exceeded: %v", ops.timeout)
			}
		}
		c := cmap[obj.Name()]
		var opstr string
		var exist bool
		if upgrade {
			var err error
			exist, err = releaseExists(m.HC, ops.k8sNamespace, ops.releaseNamePrefix+c.Title)
			if err != nil {
				return errors.Wrap(err, "error error getting release names")
			}
		}
		if upgrade && exist {
			relname, ok := upgradeMap[c.Title]
			if !ok {
				return fmt.Errorf("chart not found in release map: %v", c.Title)
			}
			opstr = "upgrade"
			uops := []helm.UpdateOption{
				// By not setting either ResetValues or ReuseValues, Helm will reuse the current release values
				// only if no values are provided in the update request
				helm.UpgradeWait(c.WaitUntilHelmSaysItsReady),
				helm.UpgradeTimeout(int64(c.WaitTimeout.Seconds())),
			}
			// work around a bug in helm 2.9 that causes a YAML error with empty overrides and ReuseValues
			vo := map[string]interface{}{}
			err := yaml.Unmarshal(c.ValueOverrides, &vo)
			if err != nil {
				return errors.Wrap(err, "error unmarshaling value overrides")
			}
			if len(vo) != 0 {
				uops = append(uops, helm.UpdateValueOverrides(c.ValueOverrides))
			}
			m.log("%v: running helm upgrade", obj.Name())
			_, err = m.HC.UpdateReleaseWithContext(ctx, relname, c.Location, uops...)
			if err != nil {
				return m.charterror(err, ops, c, "upgrading")
			}
		} else {
			opstr = "installation"
			m.log("%v: running helm install", obj.Name())
			resp, err := m.HC.InstallReleaseWithContext(ctx, c.Location, ops.k8sNamespace,
				helm.ValueOverrides(c.ValueOverrides),
				helm.ReleaseName(ReleaseName(ops.releaseNamePrefix+c.Title)),
				helm.InstallWait(c.WaitUntilHelmSaysItsReady),
				helm.InstallTimeout(int64(c.WaitTimeout.Seconds())))
			if err != nil {
				return m.charterror(err, ops, c, "installing")
			}
			rn.Lock()
			rn.rmap[c.Title] = resp.Release.Name
			rn.Unlock()
		}
		m.log("%v: %v complete; waiting for health", opstr, obj.Name())
		return m.waitForChart(ctx, c, ops.k8sNamespace)
	}
	if err := og.Walk(ctx, af); err != nil {
		werr, ok := err.(dag.WalkError)
		if !ok {
			// shouldn't be possible
			return nil, errors.Wrap(err, "dag walk error (not a WalkError)")
		}
		err2 := errors.Cause(werr.Err)
		if ce, ok := err2.(ChartError); ok {
			ce.Level = werr.Level
			return nil, ce
		}
		return nil, err
	}
	return rn.rmap, nil
}

func (m *Manager) charterror(err error, ops *options, c *Chart, operation string) error {
	ce := NewChartError(err)
	if c.WaitUntilHelmSaysItsReady {
		rc, err2 := m.HC.ReleaseContent(ReleaseName(ops.releaseNamePrefix + c.Title))
		if err2 != nil || rc == nil || rc.Release == nil {
			m.log("error fetching helm release: %v", err2)
			return ce
		}
		if err2 := ce.PopulateFromRelease(rc.Release, m.K8c, MaxPodLogLines); err2 != nil {
			m.log("error populating chart error from release: %v", err2)
			return errors.Wrap(err, "error "+operation+" chart")
		}
		return ce
	}
	if err2 := ce.PopulateFromDeployment(ops.k8sNamespace, c.WaitUntilDeployment, m.K8c, MaxPodLogLines); err2 != nil {
		m.log("error populating chart error from deployment: %v", err2)
		return errors.Wrap(err, "error "+operation+" chart")
	}
	return ce
}

// ChartWaitPollInterval is the amount of time spent between polling attempts when checking if a deployment is healthy
var ChartWaitPollInterval = 10 * time.Second

func (m *Manager) waitForChart(ctx context.Context, c *Chart, ns string) error {
	defer m.log("%v: done", c.Name())
	if c.WaitUntilHelmSaysItsReady {
		m.log("%v: helm waited until it thought the chart installation was healthy; done", c.Name())
		return nil
	}
	if c.DeploymentHealthIndication == IgnorePodHealth {
		m.log("%v: IgnorePodHealth, no health check needed", c.Name())
		return nil
	}
	return wait.Poll(ChartWaitPollInterval, c.WaitTimeout, func() (bool, error) {
		d, err := m.K8c.AppsV1().Deployments(ns).Get(c.WaitUntilDeployment, metav1.GetOptions{})
		if err != nil || d.Spec.Replicas == nil {
			m.log("%v: error getting deployment (retrying): %v", c.Name(), err)
			return false, nil // the deployment may not initially exist immediately after installing chart
		}

		rs, err := deploymentutil.GetNewReplicaSet(d, m.K8c.AppsV1())
		if err != nil {
			return false, errors.Wrap(err, "error getting new replica set")
		}

		if rs != nil {
			needed := 1
			if c.DeploymentHealthIndication == AllPodsHealthy {
				needed = int(*d.Spec.Replicas)
			}
			m.log("%v: %v ready replicas, %v needed", c.Name(), rs.Status.ReadyReplicas, needed)
			return int(rs.Status.ReadyReplicas) >= needed, nil
		}
		return false, nil
	})
}

func releaseExists(helmClient HelmClient, namespace string, releaseName string) (bool, error) {
	releases, err := helmClient.ListReleases(helm.ReleaseListNamespace(namespace), helm.ReleaseListFilter("^"+releaseName+"$"))
	if err != nil {
		return false, errors.Wrap(err, "error getting release name")
	}

	return releases != nil && releases.Count == 1, nil
}

// ValidateCharts verifies that a set of charts is constructed properly, particularly with respect
// to dependencies. It does not check to see if the referenced charts exist in the local filesystem.
func ValidateCharts(charts []Chart) error {
	objs := []dag.GraphObject{}
	for i := range charts {
		if charts[i].Title == "" {
			return fmt.Errorf("empty title at offset %v", i)
		}
		if charts[i].Location == "" {
			return fmt.Errorf("empty location at offset %v", i)
		}
		switch charts[i].DeploymentHealthIndication {
		case IgnorePodHealth:
		case AllPodsHealthy:
		case AtLeastOnePodHealthy:
		default:
			return fmt.Errorf("unknown value for DeploymentHealthIndication at offset %v: %v", i, charts[i].DeploymentHealthIndication)
		}
		objs = append(objs, &charts[i])
	}
	og := dag.ObjectGraph{}
	if err := og.Build(objs); err != nil {
		return errors.Wrap(err, "error building graph from charts")
	}
	return nil
}
