package cmd

import (
	"fmt"
	"github.com/kris-nova/kubicorn/apis/cluster"
	"github.com/kris-nova/kubicorn/cutil"
	"github.com/kris-nova/kubicorn/cutil/initapi"
	"github.com/kris-nova/kubicorn/cutil/kubeconfig"
	"github.com/kris-nova/kubicorn/cutil/logger"
	"github.com/spf13/cobra"
	"k8s.io/kube-deploy/cluster-api/api"
	"os"
	"strings"
)

var createCmd = &cobra.Command{
	Use:   "create [YAML_FILE]",
	Short: "Simple kubernetes cluster creator",
	Long:  `Create a kubernetes cluster with one command`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			logger.Critical("Please provide yaml file for cluster definition.")
			os.Exit(1)
		} else if len(args) > 1 {
			logger.Critical("Too many arguments.")
			os.Exit(1)
		}
		yamlFile := args[0]
		cluster, err := ReadAndValidateYaml(yamlFile)
		if err != nil {
			logger.Critical(err.Error())
			os.Exit(1)
		}
		logger.Info("Parsing done [%s]", cluster)

		if err = createCluster(cluster); err != nil {
			logger.Critical(err.Error())
			os.Exit(1)
		}
	},
}

// createCluster uses kubicorn API to create cluster
func createCluster(cluster *api.Cluster) error {
	newCluster := ConvertToKubicornCluster(cluster)

	err := CreateKubicornCluster(newCluster)
	if err != nil {
		return err
	}

	err = kubeconfig.RetryGetConfig(newCluster)
	if err != nil {
		return fmt.Errorf("Unable to write kubeconfig: %v", err)
	}

	logger.Always("The [%s] cluster has applied successfully!", newCluster.Name)
	logger.Always("You can now `kubectl get nodes`")
	privKeyPath := strings.Replace(newCluster.SSH.PublicKeyPath, ".pub", "", 1)
	logger.Always("You can SSH into your cluster ssh -i %s %s@%s", privKeyPath, newCluster.SSH.User, newCluster.KubernetesAPI.Endpoint)

	return nil
}

func CreateKubicornCluster(cluster *cluster.Cluster) error {
	cluster, err := initapi.InitCluster(cluster)
	if err != nil {
		return err
	}
	runtimeParams := &cutil.RuntimeParameters{}
	reconciler, err := cutil.GetReconciler(cluster, runtimeParams)
	if err != nil {
		return fmt.Errorf("Unable to get reconciler: %v", err)
	}

	logger.Info("Query existing resources")
	actual, err := reconciler.Actual(cluster)
	if err != nil {
		return fmt.Errorf("Unable to get actual cluster: %v", err)
	}
	logger.Info("Resolving expected resources")
	expected, err := reconciler.Expected(cluster)
	if err != nil {
		return fmt.Errorf("Unable to get expected cluster: %v", err)
	}

	logger.Info("Reconciling")
	cluster, err = reconciler.Reconcile(actual, expected)
	if err != nil {
		return fmt.Errorf("Unable to reconcile cluster: %v", err)
	}

	return nil
}

func init() {
	RootCmd.AddCommand(createCmd)
}
