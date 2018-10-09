package cmd

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v2"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/helm/pkg/helm"
	"k8s.io/helm/pkg/helm/portforwarder"
	"k8s.io/helm/pkg/kube"
)

// ChartDefinition models a chart in the YAML input file
type ChartDefinition struct {
	// Name of the chart (must be unique)
	Name string `yaml:"name"`
	// Local filesystem path to the chart (directory or archive file)
	Path string `yaml:"path"`
	// Path to the values YAML file for overrides
	ValuesPath string `yaml:"values_path"`
	// The name of the k8s deployment object created by the chart used to determine health (omit or leave empty to ignore chart health)
	PrimaryDeployment string `yaml:"primary_deployment"`
	// How long to wait for the chart to become healthy before failing. Use a string like "10m" or "90s".
	Timeout string `yaml:"timeout"`
	// Wait for all pods of PrimaryDeployment to be healthy? If false, it will only wait for the first pod to become healthy
	WaitForAllPods bool `yaml:"wait_for_all_pods"`
	// Wait until Helm thinks the chart is ready (equivalent to the helm install --wait CLI flag). Overrides PrimaryDeployment.
	WaitForHelm bool `yaml:"wait_for_helm"`
	// The list of dependencies this chart has (names must be present in the same file)
	Dependencies []string `yaml:"dependencies"`
}

type installCfg struct {
	upgrade           bool
	tillerNS          string
	tillerTimeout     time.Duration
	k8sCtx            string
	k8sNS             string
	releaseNamePrefix string
}

var instConfig installCfg

// installCmd represents the install command
var installCmd = &cobra.Command{
	Use:   "install [options] <file>",
	Short: "Install a graph of charts",
	Long:  `Install a group of Helm charts in order according to dependency analysis.`,
	Run:   install,
}

func init() {
	installCmd.Flags().BoolVar(&instConfig.upgrade, "upgrade", false, "Upgrade release if release exists")
	installCmd.Flags().DurationVar(&instConfig.tillerTimeout, "tiller-timeout", 90*time.Second, "Tiller connect timeout")
	installCmd.Flags().StringVar(&instConfig.tillerNS, "tiller-namespace", "kube-system", "k8s namespace where Tiller can be found")
	installCmd.Flags().StringVar(&instConfig.k8sNS, "k8s-namespace", "", "k8s namespace into which to install charts")
	installCmd.Flags().StringVar(&instConfig.k8sCtx, "k8s-ctx", "", "k8s context")
	installCmd.Flags().StringVar(&instConfig.releaseNamePrefix, "release-name-prefix", "", "Release name prefix")
	RootCmd.AddCommand(installCmd)
}

func validateChart(c ChartDefinition) error {
	if c.Name == "" {
		return errors.New("name is empty")
	}
	if c.Path == "" {
		return errors.New("path is empty")
	}
	if _, err := os.Stat(c.Path); err != nil {
		return errors.Wrap(err, "error with path")
	}
	if c.ValuesPath != "" {
		if _, err := os.Stat(c.ValuesPath); err != nil {
			return errors.Wrap(err, "error with values_path")
		}
	}
	if c.Timeout != "" {
		if _, err := time.ParseDuration(c.Timeout); err != nil {
			return errors.Wrap(err, "error with timeout")
		}
	}
	for i, d := range c.Dependencies {
		if len(d) == 0 {
			return fmt.Errorf("empty string in dependencies at offset %v", i)
		}
	}
	return nil
}

func readAndValidateFile(f string, validate bool) ([]ChartDefinition, error) {
	b, err := ioutil.ReadFile(f)
	if err != nil {
		return nil, errors.Wrap(err, "error reading file")
	}
	charts := []ChartDefinition{}
	if err := yaml.Unmarshal(b, &charts); err != nil {
		return nil, errors.Wrap(err, "error unmarshaling YAML")
	}
	if len(charts) == 0 {
		return nil, errors.New("file is empty")
	}

	baseDir := filepath.Dir(f)
	expandChartFilesPath(charts, baseDir)

	if validate {
		for i, c := range charts {
			if err := validateChart(c); err != nil {
				return nil, errors.Wrapf(err, "error validating chart at offset %v", i)
			}
		}
	}
	return charts, nil
}

// expandChartFilesPath expands relative file path for charts and values
func expandChartFilesPath(charts []ChartDefinition, baseDir string) {
	for i := range charts {
		c := &charts[i]
		c.ValuesPath = expandFilePath(c.ValuesPath, baseDir)
		c.Path = expandFilePath(c.Path, baseDir)
	}
}

// expandFilePath expands relative file path using specified base directory
func expandFilePath(filePath string, baseDir string) string {
	if !strings.HasPrefix(filePath, "/") {
		filePath = path.Join(baseDir, filePath)
	}
	return filePath
}

func chartDefToChart(cd ChartDefinition) (metahelm.Chart, error) {
	var b []byte
	var err error
	if cd.ValuesPath != "" {
		b, err = ioutil.ReadFile(cd.ValuesPath)
		if err != nil {
			return metahelm.Chart{}, errors.Wrap(err, "error reading values file")
		}
	}
	var wt time.Duration
	dhi := metahelm.IgnorePodHealth
	if cd.PrimaryDeployment != "" {
		if cd.WaitForAllPods {
			dhi = metahelm.AllPodsHealthy
		} else {
			dhi = metahelm.AtLeastOnePodHealthy
		}
	}
	if cd.Timeout != "" {
		wt, err = time.ParseDuration(cd.Timeout)
		if err != nil {
			return metahelm.Chart{}, errors.Wrap(err, "error parsing timeout")
		}
	}
	if cd.WaitForHelm {
		cd.PrimaryDeployment = ""
		dhi = metahelm.IgnorePodHealth
	}
	return metahelm.Chart{
		Title:                      cd.Name,
		Location:                   cd.Path,
		ValueOverrides:             b,
		WaitUntilHelmSaysItsReady:  cd.WaitForHelm,
		WaitUntilDeployment:        cd.PrimaryDeployment,
		WaitTimeout:                wt,
		DeploymentHealthIndication: dhi,
		DependencyList:             cd.Dependencies,
	}, nil
}

func cd2c(cds []ChartDefinition) ([]metahelm.Chart, error) {
	cs := []metahelm.Chart{}
	for _, cd := range cds {
		c, err := chartDefToChart(cd)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// getK8sConfig returns a Kubernetes client config for a given context.
func getK8sConfig(context string) clientcmd.ClientConfig {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if context != "" {
		overrides.CurrentContext = context
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
}

// getKubeClient creates a Kubernetes config and client for a given kubeconfig context.
func getKubeClient(context string) (*rest.Config, kubernetes.Interface, error) {
	config, err := getK8sConfig(context).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("could not get Kubernetes config for context %q: %s", context, err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get Kubernetes client: %s", err)
	}
	return config, client, nil
}

func getClients(kctx string) (*kube.Tunnel, kubernetes.Interface, *helm.Client, error) {
	config, client, err := getKubeClient(kctx)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error getting kube client")
	}

	tunnel, err := portforwarder.New(instConfig.tillerNS, client, config)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error establishing k8s tunnel")
	}
	tillerHost := fmt.Sprintf("127.0.0.1:%d", tunnel.Local)

	return tunnel, client, helm.NewClient(helm.Host(tillerHost), helm.ConnectTimeout(int64(instConfig.tillerTimeout.Seconds()))), nil
}

func install(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		clierr("input file is required")
	}
	fp := args[len(args)-1]
	cds, err := readAndValidateFile(fp, true)
	if err != nil {
		clierr("error reading input: %v", err)
	}
	cs, err := cd2c(cds)
	if err != nil {
		clierr("error converting chart definitions: %v", err)
	}
	tunnel, kc, hc, err := getClients(instConfig.k8sCtx)
	if err != nil {
		clierr("error getting kube and helm clients: %v", err)
	}
	defer tunnel.Close()
	m := metahelm.Manager{
		HC:   hc,
		K8c:  kc,
		LogF: log.Printf,
	}
	var rm metahelm.ReleaseMap
	if instConfig.upgrade {
		rm = buildReleaseMap(instConfig, cs)
		err = m.Upgrade(context.Background(), rm, cs, instConfig.ToInstallOptions()...)
	} else {
		rm, err = m.Install(context.Background(), cs, instConfig.ToInstallOptions()...)
	}

	if err != nil {
		if ce, ok := err.(metahelm.ChartError); ok {
			displayChartError(ce)
		}
		fmt.Fprintf(os.Stderr, "error running installations: %v\n", err)
		return
	}
	for k, v := range rm {
		fmt.Printf("Chart: %v => release: %v\n", k, v)
	}
}

// buildReleaseMap build the release title to releaseName map using user input and charts definitions
func buildReleaseMap(instConfig installCfg, cs []metahelm.Chart) metahelm.ReleaseMap {
	rm := make(map[string]string)
	for _, c := range cs {
		if _, ok := rm[c.Title]; !ok {
			rm[c.Title] = metahelm.ReleaseName(instConfig.releaseNamePrefix + c.Title)
		}
	}
	return rm
}

func (instConfig *installCfg) ToInstallOptions() []metahelm.InstallOption {
	var options []metahelm.InstallOption
	if instConfig.tillerNS != "" {
		options = append(options, metahelm.WithTillerNamespace(instConfig.tillerNS))
	}
	if instConfig.k8sNS != "" {
		options = append(options, metahelm.WithK8sNamespace(instConfig.k8sNS))
	}
	return options
}

func displayChartError(ce metahelm.ChartError) {
	printFailedPods := func(t, k string, v []metahelm.FailedPod) {
		fmt.Printf("%v: %v\n", t, k)
		for _, fp := range v {
			fmt.Printf("\tPod: %v\n", fp.Name)
			fmt.Printf("\tPhase: %v\n", fp.Phase)
			fmt.Printf("\tReason: %v\n", fp.Reason)
			fmt.Printf("\tMessage: %v\n", fp.Message)
			fmt.Printf("\tConditions: %+v\n", fp.Conditions)
			fmt.Printf("\tContainer Statuses: %+v\n", fp.ContainerStatuses)
			fmt.Printf("\tContainer Logs:")
			if len(fp.Logs) == 0 {
				fmt.Printf(" <none>")
			}
			fmt.Printf("\n")
			for name, logs := range fp.Logs {
				fmt.Printf("\t\tContainer: %v\n", name)
				fmt.Printf("\t\tLogs:\n")
				if len(logs) > 0 {
					fmt.Printf("\n====LOG START====\n")
					os.Stdout.Write(logs)
					fmt.Printf("\n====LOG END====\n")
				} else {
					fmt.Printf("<empty>\n")
				}
				fmt.Printf("\n\n")
			}
		}
	}
	if len(ce.FailedDeployments) > 0 {
		fmt.Printf("FAILED DEPLOYMENTS:\n===================\n")
		for k, v := range ce.FailedDeployments {
			printFailedPods("Deployment", k, v)
		}
	}
	if len(ce.FailedJobs) > 0 {
		fmt.Printf("FAILED JOBS:\n===================\n")
		for k, v := range ce.FailedJobs {
			printFailedPods("Job", k, v)
		}
	}
	if len(ce.FailedDaemonSets) > 0 {
		fmt.Printf("FAILED DAEMONSETS:\n===================\n")
		for k, v := range ce.FailedDaemonSets {
			printFailedPods("DaemonSet", k, v)
		}
	}
}
