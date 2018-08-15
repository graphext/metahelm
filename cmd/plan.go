package cmd

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"runtime"

	"github.com/dollarshaveclub/metahelm/pkg/dag"
	"github.com/spf13/cobra"
)

// planCmd represents the plan command
var planCmd = &cobra.Command{
	Use:   "plan <file>.yml",
	Short: "Display installation info about a graph of charts",
	Long: `Generates the dependency graph from a list of charts and displays the installation
order with graph levels, and optionally a visual image of the graph.`,
	Run: plan,
}

var opencmd = "<unknown>"
var dotcmd string
var genpng, validate bool

func init() {
	switch runtime.GOOS {
	case "darwin":
		opencmd = "open"
	case "linux":
		opencmd = "xdg-open"
	case "windows":
		opencmd = "start"
	}
	planCmd.Flags().StringVar(&opencmd, "open-cmd", opencmd, "open CLI command")
	planCmd.Flags().StringVar(&dotcmd, "dot-cmd", "dot", "dot CLI command (to generate PNG)")
	planCmd.Flags().BoolVarP(&genpng, "gen-png", "g", false, "generate and display PNG graph")
	planCmd.Flags().BoolVar(&validate, "validate", true, "validate charts")
	RootCmd.AddCommand(planCmd)
}

func plan(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		clierr("input file is required")
	}
	fp := args[len(args)-1]
	cds, err := readAndValidateFile(fp, validate)
	if err != nil {
		clierr("error reading input: %v", err)
	}
	cs, err := cd2c(cds)
	if err != nil {
		clierr("error converting chart definitions: %v", err)
	}
	objs := []dag.GraphObject{}
	for i := range cs {
		objs = append(objs, &cs[i])
	}
	og := dag.ObjectGraph{}
	err = og.Build(objs)
	if err != nil {
		clierr("object graph error: %v", err)
	}
	r, lvls, err := og.Info()
	if err != nil {
		clierr("error getting graph info: %v", err)
	}
	fmt.Printf("Graph Root: %v\n", r.Name())
	j := 1
	for i := len(lvls) - 1; i >= 0; i-- {
		fmt.Printf("Phase %v: %v\n", j, lvls[i])
		j++
	}
	if genpng {
		b, err := og.Dot("metahelm_plan")
		if err != nil {
			clierr("error generating dot output: %v", err)
		}
		f, err := ioutil.TempFile("", "metahelm-plan")
		if err != nil {
			clierr("error getting temp file: %v", err)
		}
		f.Write(b)
		f.Close()
		fn := f.Name()
		fmt.Fprintf(os.Stderr, "wrote dot output to %v\n", f.Name())
		cmd := fmt.Sprintf("dot %v -Tpng -o %v.png && %v %v.png", fn, fn, opencmd, fn)
		fmt.Fprintf(os.Stderr, "running /bin/bash -c %v\n", cmd)
		c := exec.Command("/bin/bash", "-c", cmd)
		c.Run()
		eo, _ := c.CombinedOutput()
		if len(eo) > 0 {
			fmt.Fprintf(os.Stderr, string(eo)+"\n")
		}
	}
}
