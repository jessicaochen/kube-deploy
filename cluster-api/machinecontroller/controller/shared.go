package controller

import (
	"fmt"
	"io/ioutil"
	"github.com/ghodss/yaml"
	"github.com/kris-nova/kubicorn/cutil"
	"github.com/kris-nova/kubicorn/cutil/initapi"
	"github.com/kris-nova/kubicorn/cutil/logger"
	"github.com/kris-nova/kubicorn/apis/cluster"
	"k8s.io/kube-deploy/cluster-api/api"
	"github.com/kris-nova/kubicorn/profiles"
)

// TODO: move logic here to shared package for cmd and controller
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

func ConvertToKubicornCluster(cluster *api.Cluster) *cluster.Cluster {
	newCluster := profileMapIndexed[cluster.Spec.Cloud](cluster.Name)
	newCluster.Name = cluster.Name
	newCluster.CloudId = cluster.Spec.Project
	newCluster.SSH.User = cluster.Spec.SSH.User
	newCluster.SSH.PublicKeyPath = cluster.Spec.SSH.PublicKeyPath
	return newCluster
}

func ReadAndValidateClusterYaml(file string) (*api.Cluster, error) {
	bytes, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}

	clusterSpec := &api.Cluster{}
	err = yaml.Unmarshal(bytes, clusterSpec)
	if err != nil {
		return nil, err
	}

	if _, ok := profileMapIndexed[clusterSpec.Spec.Cloud]; !ok {
		return nil, fmt.Errorf("invalid cloud option [%s]", clusterSpec.Spec.Cloud)
	}
	if clusterSpec.Spec.Cloud == cluster.CloudGoogle && clusterSpec.Spec.Project == "" {
		return nil, fmt.Errorf("CloudID is required for google cloud. Please set it to your project ID")
	}
	return clusterSpec, nil
}

type profileFunc func(name string) *cluster.Cluster

var profileMapIndexed = map[string]profileFunc{
	"azure":        profiles.NewUbuntuAzureCluster,
	"azure-ubuntu": profiles.NewUbuntuAzureCluster,
	"google":       profiles.NewUbuntuGoogleComputeCluster,
	"gcp":          profiles.NewUbuntuGoogleComputeCluster,
}