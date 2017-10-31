package controller

import (
	apiv1 "k8s.io/api/core/v1"
	"github.com/ghodss/yaml"
	"k8s.io/apimachinery/pkg/fields"
	"github.com/golang/glog"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	machinesv1 "k8s.io/kube-deploy/cluster-api/api/machines/v1alpha1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"context"
	"github.com/kris-nova/kubicorn/apis/cluster"
)

type MachineController struct {
	Config *Configuration
	restClient *rest.RESTClient
	crdClient *crdclient
}

func (c *MachineController) Run () error {
	glog.Infof("Woot")

	restClient, schema, err := restClient(c.Config.Kubeconfig)
	if err != nil {
		glog.Fatalf("error creating rest client: %v", err)
	}

	c.restClient = restClient
	c.crdClient = newCrdClient(restClient, schema)

	// Run leader election
	return c.run(context.Background())
}

func (c *MachineController) run(ctx context.Context) error {
	source := cache.NewListWatchFromClient(c.restClient, "machines", apiv1.NamespaceAll, fields.Everything())

	_, informer := cache.NewInformer(
		source,
		&machinesv1.Machine{},
		0,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    c.onAdd,
			UpdateFunc: c.onUpdate,
			DeleteFunc: c.onDelete,
		},
	)

	informer.Run(ctx.Done())
	return nil
}

func (c *MachineController) onAdd(obj interface{}) {
	machine := obj.(*machinesv1.Machine)
	glog.Infof("object created: %s\n", machine.ObjectMeta.Name)
	c.reconcileCluster()
}

func (c *MachineController) onUpdate(oldObj, newObj interface{}) {
	oldMachine := oldObj.(*machinesv1.Machine)
	newMachine := newObj.(*machinesv1.Machine)
	glog.Infof("object updated: %s\n", oldMachine.ObjectMeta.Name)
	glog.Infof("  old k8s version: %s, new: %s\n", oldMachine.Spec.Versions.Kubelet, newMachine.Spec.Versions.Kubelet)
	c.reconcileCluster()
}

func (c *MachineController) onDelete(obj interface{}) {
	machine := obj.(*machinesv1.Machine)
	glog.Infof("object deleted: %s\n", machine.ObjectMeta.Name)
	c.reconcileCluster()
}

// Reconcile the cluster with the current CRDs
// Note that this is the easiest method with Kubicorn
func(c *MachineController) reconcileCluster() error {
	kubicornCluster := c.getKubicornCluster()
	// Creating and reconciling are the same for kubicorn.
	return CreateKubicornCluster(kubicornCluster)
}

func(c *MachineController) getKubicornCluster() *cluster.Cluster {
	// Empty cluster-level cluster
	// TODO: Get apiCluster from the cluster CRD when that is ready
	apiCluster, err := ReadAndValidateClusterYaml(c.Config.Clusterconfig)
	if err != nil {
		glog.Fatalf("error reading clutser config: %v", err)
	}
	kubicornCluster := ConvertToKubicornCluster(apiCluster)

	// Fill in machines
	// To handle model mismatch we model each
	// individual machine as a kubicorn server pool.
	machineList, err := c.crdClient.List(meta_v1.ListOptions{})
	if err != nil {
		glog.Fatalf("error fetching machine list: %v", err)
	}

	serverpools := []*cluster.ServerPool{}
	for _, machine := range machineList.Items {
		serverpools = append(serverpools, convertToKubicornServerPool(&machine))
	}
	kubicornCluster.ServerPools = serverpools
	return kubicornCluster
}

func convertToKubicornServerPool(machine *machinesv1.Machine) *cluster.ServerPool{
	 // The serverpool definition is crammed into the provider spec
	 var sp cluster.ServerPool
	 err := yaml.Unmarshal([]byte(machine.Spec.ProviderConfig), &sp)
	 if err != nil {
		glog.Fatalf("error reading kubicorn serverpool config from machine: %v", err)
	 }

	 // An machine is really a single node serverpool
	 sp.MinCount = 1
	 sp.MaxCount = 1

	 return &sp
}