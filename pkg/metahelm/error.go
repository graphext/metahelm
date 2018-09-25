package metahelm

import (
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/helm/pkg/manifest"
	"k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/releaseutil"
	deploymentutil "k8s.io/kubernetes/pkg/controller/deployment/util"
)

type FailedPod struct {
	Name, Phase, Message, Reason string
	Conditions                   []corev1.PodCondition
	ContainerStatuses            []corev1.ContainerStatus
	// Logs is a map of container name to raw log (stdout) output
	Logs map[string][]byte
}

// ChartError is a chart install/upgrade error due to failing Kubernetes resources
type ChartError struct {
	FailedPods        []FailedPod
	FailedDaemonSets  map[string][]FailedPod
	FailedDeployments map[string][]FailedPod
	FailedJobs        map[string][]FailedPod
}

// NewChartError returns an initialized empty ChartError
func NewChartError() ChartError {
	return ChartError{
		FailedPods:        []FailedPod{},
		FailedDaemonSets:  make(map[string][]FailedPod),
		FailedDeployments: make(map[string][]FailedPod),
		FailedJobs:        make(map[string][]FailedPod),
	}
}

// Error satisfies the error interface
func (ce ChartError) Error() string {
	return fmt.Sprintf("error installing/upgrading chart: failed resources: deployments: %v; jobs: %v; pods: %v; daemonsets: %v", len(ce.FailedDeployments), len(ce.FailedJobs), len(ce.FailedPods), len(ce.FailedDaemonSets))
}

// PopulateFromRelease finds the failed Jobs and Pods for a given release and fills ChartError with names and logs of the failed resources
func (ce ChartError) PopulateFromRelease(rls *release.Release, kc K8sClient, maxloglines uint) error {
	var maxlines *int64
	if maxloglines > 0 {
		ml := int64(maxloglines)
		maxlines = &ml
	}
	plopts := corev1.PodLogOptions{
		TailLines: maxlines,
	}
	if rls == nil {
		return errors.New("release is nil")
	}
	for _, m := range manifest.SplitManifests(releaseutil.SplitManifests(rls.Manifest)) {
		ss := []string{}
		failedpods := []FailedPod{}
		switch m.Head.Kind {
		case "Deployment":
			d, err := kc.ExtensionsV1beta1().Deployments(rls.Namespace).Get(m.Name, metav1.GetOptions{})
			if err != nil || d.Spec.Replicas == nil || d == nil {
				return errors.Wrap(err, "error getting deployment")
			}
			rs, err := deploymentutil.GetNewReplicaSet(d, kc.ExtensionsV1beta1())
			if err != nil || rs == nil {
				return errors.Wrap(err, "error replica set")
			}
			for k, v := range d.Spec.Selector.MatchLabels {
				ss = append(ss, fmt.Sprintf("%v = %v", k, v))
			}
		case "Job":
			j, err := kc.BatchV1().Jobs(rls.Namespace).Get(m.Name, metav1.GetOptions{})
			if err != nil {
				return errors.Wrap(err, "error getting job")
			}
			ss := []string{}
			for k, v := range j.Spec.Selector.MatchLabels {
				ss = append(ss, fmt.Sprintf("%v = %v", k, v))
			}
		case "DaemonSet":
			ds, err := kc.ExtensionsV1beta1().DaemonSets(rls.Namespace).Get(m.Name, metav1.GetOptions{})
			if err != nil {
				return errors.Wrap(err, "error getting daemonset")
			}
			ss := []string{}
			for k, v := range ds.Spec.Selector.MatchLabels {
				ss = append(ss, fmt.Sprintf("%v = %v", k, v))
			}
		case "Pod":
		default:
			// we don't care about any other resource types
			continue
		}
		if len(ss) == 0 {
			continue
		}
		pl, err := kc.CoreV1().Pods(rls.Namespace).List(metav1.ListOptions{LabelSelector: strings.Join(ss, ",")})
		if err != nil || pl == nil {
			return errors.Wrapf(err, "error listing pods for selector: %v", ss)
		}
		for _, pod := range pl.Items {
			if pod.Status.Phase != corev1.PodRunning || pod.Status.Phase != corev1.PodSucceeded {
				fp := FailedPod{Logs: make(map[string][]byte)}
				fp.Name = pod.ObjectMeta.Name
				fp.Phase = string(pod.Status.Phase)
				fp.Message = pod.Status.Message
				fp.Reason = pod.Status.Reason
				fp.Conditions = pod.Status.Conditions
				fp.ContainerStatuses = pod.Status.ContainerStatuses
				// get logs
				for _, cs := range fp.ContainerStatuses {
					if !cs.Ready && cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.ExitCode != 0 {
						plopts.Container = cs.Name
						req := kc.CoreV1().Pods(rls.Namespace).GetLogs(pod.Name, &plopts)
						logrc, err := req.Stream()
						if err != nil {
							return errors.Wrapf(err, "error getting logs for pod: %v", pod.Name)
						}
						logs, err := ioutil.ReadAll(logrc)
						if err != nil {
							logrc.Close()
							return errors.Wrapf(err, "error reading logs for pod: %v", pod.Name)
						}
						logrc.Close()
						fp.Logs[cs.Name] = logs
					}
				}
				failedpods = append(failedpods, fp)
			}
		}
		switch m.Head.Kind {
		case "Deployment":
			ce.FailedDeployments[m.Name] = failedpods
		case "Job":
			ce.FailedJobs[m.Name] = failedpods
		case "DaemonSet":
			ce.FailedDaemonSets[m.Name] = failedpods
		case "Pod":
			ce.FailedPods = failedpods
		}
	}
	return nil
}

// PopulateFromDeployment finds the failed pods for a deployment and fills ChartError with names and logs of the failed pods
func (ce ChartError) PopulateFromDeployment(namespace, deploymentName string, kc K8sClient, maxloglines uint) error {
	var maxlines *int64
	if maxloglines > 0 {
		ml := int64(maxloglines)
		maxlines = &ml
	}
	plopts := corev1.PodLogOptions{
		TailLines: maxlines,
	}
	d, err := kc.ExtensionsV1beta1().Deployments(namespace).Get(deploymentName, metav1.GetOptions{})
	if err != nil || d.Spec.Replicas == nil || d == nil {
		return errors.Wrap(err, "error getting deployment")
	}
	rs, err := deploymentutil.GetNewReplicaSet(d, kc.ExtensionsV1beta1())
	if err != nil || rs == nil {
		return errors.Wrap(err, "error replica set")
	}
	ss := []string{}
	for k, v := range d.Spec.Selector.MatchLabels {
		ss = append(ss, fmt.Sprintf("%v = %v", k, v))
	}
	pl, err := kc.CoreV1().Pods(namespace).List(metav1.ListOptions{LabelSelector: strings.Join(ss, ",")})
	if err != nil || pl == nil {
		return errors.Wrapf(err, "error listing pods for selector: %v", ss)
	}
	failedpods := []FailedPod{}
	for _, pod := range pl.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.Status.Phase != corev1.PodSucceeded {
			fp := FailedPod{Logs: make(map[string][]byte)}
			fp.Name = pod.ObjectMeta.Name
			fp.Phase = string(pod.Status.Phase)
			fp.Message = pod.Status.Message
			fp.Reason = pod.Status.Reason
			fp.Conditions = pod.Status.Conditions
			fp.ContainerStatuses = pod.Status.ContainerStatuses
			// get logs
			for _, cs := range fp.ContainerStatuses {
				if !cs.Ready && cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.ExitCode != 0 {
					plopts.Container = cs.Name
					req := kc.CoreV1().Pods(namespace).GetLogs(pod.Name, &plopts)
					logrc, err := req.Stream()
					if err != nil {
						return errors.Wrapf(err, "error getting logs for pod: %v", pod.Name)
					}
					logs, err := ioutil.ReadAll(logrc)
					if err != nil {
						logrc.Close()
						return errors.Wrapf(err, "error reading logs for pod: %v", pod.Name)
					}
					logrc.Close()
					fp.Logs[cs.Name] = logs
				}
			}
			failedpods = append(failedpods, fp)
		}
	}
	ce.FailedDeployments[deploymentName] = failedpods
	return nil
}
