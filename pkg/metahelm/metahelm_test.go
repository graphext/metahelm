package metahelm

import (
	"context"
	"testing"
	"time"

	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	mtypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/helm/pkg/helm"
)

var testCharts = []Chart{
	Chart{
		Title:                      "toplevel",
		Location:                   "/foo",
		WaitUntilDeployment:        "toplevel",
		DeploymentHealthIndication: IgnorePodHealth,
		DependencyList:             []string{"someservice", "anotherthing", "redis"},
	},
	Chart{
		Title:                      "someservice",
		Location:                   "/foo",
		WaitUntilDeployment:        "someservice",
		DeploymentHealthIndication: IgnorePodHealth,
	},
	Chart{
		Title:                      "anotherthing",
		Location:                   "/foo",
		WaitUntilDeployment:        "anotherthing",
		DeploymentHealthIndication: AllPodsHealthy,
		WaitTimeout:                2 * time.Second,
		DependencyList:             []string{"redis"},
	},
	Chart{
		Title:                      "redis",
		Location:                   "/foo",
		DeploymentHealthIndication: IgnorePodHealth,
	},
}

func gentestobjs() []runtime.Object {
	objs := []runtime.Object{}
	reps := int32(1)
	iscontroller := true
	rsl := v1beta1.ReplicaSetList{Items: []v1beta1.ReplicaSet{}}
	for _, c := range testCharts {
		r := &v1beta1.ReplicaSet{}
		d := &v1beta1.Deployment{}
		d.Spec.Replicas = &reps
		d.Spec.Template.Labels = map[string]string{"app": c.Name()}
		d.Spec.Template.Spec.NodeSelector = map[string]string{}
		d.Spec.Template.Name = c.Name()
		d.Spec.Selector = &metav1.LabelSelector{}
		d.Spec.Selector.MatchLabels = map[string]string{"app": c.Name()}
		r.Spec.Selector = d.Spec.Selector
		r.Spec.Replicas = &reps
		r.Status.ReadyReplicas = 1
		r.Name = "replicaset-" + c.Name()
		r.Namespace = DefaultK8sNamespace
		r.Labels = d.Spec.Template.Labels
		d.Labels = d.Spec.Template.Labels
		d.ObjectMeta.UID = mtypes.UID(c.Name() + "-deployment")
		r.ObjectMeta.OwnerReferences = []metav1.OwnerReference{metav1.OwnerReference{UID: d.ObjectMeta.UID, Controller: &iscontroller}}
		d.Name = c.Name()
		d.Namespace = DefaultK8sNamespace
		r.Spec.Template = d.Spec.Template
		objs = append(objs, d)
		rsl.Items = append(rsl.Items, *r)
	}
	return append(objs, &rsl)
}

func TestGraphInstall(t *testing.T) {
	fkc := fake.NewSimpleClientset(gentestobjs()...)
	fhc := &helm.FakeClient{}
	m := Manager{
		LogF: t.Logf,
		K8c:  fkc,
		HC:   fhc,
	}
	chartWaitPollInterval = 1 * time.Second
	rm, err := m.Install(context.Background(), testCharts)
	if err != nil {
		t.Fatalf("error installing: %v", err)
	}
	t.Logf("rm: %v\n", rm)
}

func TestGraphInstallWaitCallback(t *testing.T) {
	fkc := fake.NewSimpleClientset(gentestobjs()...)
	fhc := &helm.FakeClient{}
	m := Manager{
		K8c: fkc,
		HC:  fhc,
	}
	chartWaitPollInterval = 1 * time.Second
	var i int
	cb := func(c Chart) InstallCallbackAction {
		if c.Name() != testCharts[1].Name() {
			return Continue
		}
		if i >= 2 {
			return Continue
		}
		i++
		return Wait
	}
	retryDelay = 10 * time.Millisecond
	_, err := m.Install(context.Background(), testCharts, WithInstallCallback(cb))
	if err != nil {
		t.Fatalf("error installing: %v", err)
	}
	if i < 2 {
		t.Fatalf("bad callback count: %v", i)
	}
}

func TestGraphInstallAbortCallback(t *testing.T) {
	fkc := fake.NewSimpleClientset(gentestobjs()...)
	fhc := &helm.FakeClient{}
	m := Manager{
		K8c: fkc,
		HC:  fhc,
	}
	chartWaitPollInterval = 1 * time.Second
	var i int
	cb := func(c Chart) InstallCallbackAction {
		i++
		if c.Name() == testCharts[3].Name() {
			return Abort
		}
		return Continue
	}
	retryDelay = 10 * time.Millisecond
	_, err := m.Install(context.Background(), testCharts, WithInstallCallback(cb))
	if err == nil {
		t.Fatalf("should have failed")
	}
	if i != 1 {
		t.Fatalf("bad callback count: %v", i)
	}
}
