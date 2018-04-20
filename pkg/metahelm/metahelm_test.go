package metahelm

import "testing"

var testCharts = []Chart{
	Chart{
		Title:                      "toplevel",
		Location:                   "/foo",
		WaitUntilDeployment:        "toplevel",
		DeploymentHealthIndication: AllPodsHealthy,
		DependencyList:             []string{"someservice", "anotherthing", "redis"},
	},
	Chart{
		Title:                      "someservice",
		Location:                   "/foo",
		WaitUntilDeployment:        "someservice",
		DeploymentHealthIndication: AllPodsHealthy,
	},
	Chart{
		Title:                      "anotherthing",
		Location:                   "/foo",
		WaitUntilDeployment:        "anotherthing",
		DeploymentHealthIndication: AtLeastOnePodHealthy,
		DependencyList:             []string{"redis"},
	},
	Chart{
		Title:                      "redis",
		Location:                   "/foo",
		DeploymentHealthIndication: IgnorePodHealth,
	},
}

type fakeK8sClient struct {
}

type fakeHelmClient struct {
}

func TestGraphInstall(t *testing.T) {

}
