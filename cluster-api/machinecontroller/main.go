package main

import (
	"fmt"
	"os"
	"k8s.io/apiserver/pkg/util/flag"
	"k8s.io/kubernetes/pkg/version/verflag"
	"k8s.io/apiserver/pkg/util/logs"
	"github.com/spf13/pflag"
	"k8s.io/kube-deploy/cluster-api/machinecontroller/controller"

)

func main() {
	c := controller.NewConfiguration()
	c.AddFlags(pflag.CommandLine)

	flag.InitFlags()
	logs.InitLogs()
	defer logs.FlushLogs()

	verflag.PrintAndExitIfRequested()

	if err := controller.Run(c); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
