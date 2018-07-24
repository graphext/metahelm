package metahelm

import (
	"fmt"
	"time"
)

// HealthIndication describes how to decide if a deployment is successful
type HealthIndication int

const (
	// IgnorePodHealth indicates that we don't care about pod health
	IgnorePodHealth HealthIndication = iota
	// AllPodsHealthy indeicates that all pods are OK
	AllPodsHealthy
	// AtLeastOnePodHealthy indicates >= 1 pods are OK
	AtLeastOnePodHealthy
)

// DefaultDeploymentTimeout indicates the default time to wait for a deployment to be healthy
const DefaultDeploymentTimeout = 10 * time.Minute

// Chart models a single installable Helm chart
type Chart struct {
	Title                      string           // unique name for this chart (must not collide with any dependencies)
	Location                   string           // local filesystem location
	ValueOverrides             []byte           // value overrides as raw YAML stream
	WaitUntilHelmSaysItsReady  bool             // wait until Helm thinks the chart is ready. This overrides WaitUntilDeployment and DeploymentHealthIndication.
	WaitUntilDeployment        string           // Deployment name that, when healthy, indicates chart install has succeeded
	WaitTimeout                time.Duration    // how long to wait for the deployment to become healthy. If unset, DefaultDeploymentTimeout is used
	DeploymentHealthIndication HealthIndication // How to determine if a deployment is healthy
	DependencyList             []string
}

func (c *Chart) Name() string {
	return c.Title
}

func (c *Chart) String() string {
	return fmt.Sprintf("\"%v\"", c.Title) // quoted to avoid DOT format problems
}

func (c *Chart) Dependencies() []string {
	return c.DependencyList
}
