/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package deploy

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
//   "os/exec"
//  "strings"
	"time"
//	"bufio"
//	"strconv"

	"github.com/golang/glog"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
		"k8s.io/kube-deploy/cluster-api/pkg/client/clientset_generated/clientset"
	clusterv1 "k8s.io/kube-deploy/cluster-api/pkg/apis/cluster/v1alpha1"
	"k8s.io/kube-deploy/cluster-api/util"
//	google "k8s.io/kube-deploy/cluster-api/cloud/google"
)

const (
	MasterIPAttempts       = 40
	SleepSecondsPerAttempt = 5
	ServiceAccountNs       = "kube-system"
	ServiceAccountName     = "default"
)

func (d *deployer) createCluster(c *clusterv1.Cluster, machines []*clusterv1.Machine, vmCreated *bool) error {
	if c.GetName() == "" {
		return fmt.Errorf("cluster name must be specified for cluster creation")
	}
	master := util.GetMaster(machines)
	if master == nil {
		return fmt.Errorf("master spec must be provided for cluster creation")
	}

	if master.GetName() == "" && master.GetGenerateName() == "" {
		return fmt.Errorf("master name must be specified for cluster creation")
	}

	if master.GetName() == "" {
		master.Name = master.GetGenerateName() + c.GetName()
	}

	glog.Infof("Installing local minikube")
	glog.Infof(util.ExecCommand("rm", "-rf", "minikube"))
	glog.Infof(util.ExecCommand("rm", "-rf", ".minikube"))
	glog.Infof(util.ExecCommand("curl", "-Lo", "minikube", "https://storage.googleapis.com/minikube/releases/v0.25.2/minikube-linux-amd64"))
	glog.Infof(util.ExecCommand("chmod","+x","minikube"))

	glog.Infof("Starting local minikube")
	glog.Infof(util.ExecCommand("./minikube","start","--vm-driver=kvm2", "--bootstrapper=kubeadm"))


  glog.Infof("Applying Cluster API apiserver")
	if err := d.machineDeployer.CreateAPIServer(); err != nil {
		return fmt.Errorf("can't create Cluster API apiserver: %v", err)
	}

  glog.Infof("Applying machine controllers")
	if err := d.machineDeployer.CreateControllers(c, machines); err != nil {
		return fmt.Errorf("can't create machine controller: %v", err)
	}

  // Switch client context to talk to bootstrap cluster
	if err := d.initApiClient(); err != nil {
		return err
	}
  bootstrapClientSet, err := util.NewClientSet(d.configPath)
  if err != nil {
		return fmt.Errorf("Could not create client: %s", err)
  }

	if err := d.waitForClusterResourceReady(); err != nil {
  		return err
  }

 	glog.Infof("Starting cluster creation %s", c.GetName())
	createClusterErr := util.Retry(func() (bool, error) {
		glog.Infof("Waiting for Kubernetes to come up...")
		cluster, err := d.client.Clusters(apiv1.NamespaceDefault).Create(c);
		if err != nil {
			glog.Errorf("Error while applying %s", err)
			return false, nil
		}
		glog.Infof("Applied.")
		c = cluster
		return true, nil
	}, 5)

	if createClusterErr != nil {
		return fmt.Errorf("timedout applying CLuster: %s", createClusterErr)
	}

  glog.Infof("Starting master creation %s", master.GetName())

	if err := d.createMachine(master); err != nil {
		return err
	}

	*vmCreated = true
	glog.Infof("Created master %s", master.GetName())

	masterIP, err := d.getMasterIP(master)
	if err != nil {
		return fmt.Errorf("unable to get master IP: %v", err)
	}


	c.Status.APIEndpoints = append(c.Status.APIEndpoints,
		clusterv1.APIEndpoint{
			Host: masterIP,
			Port: 443,
		})

	if c, err = d.client.Clusters(apiv1.NamespaceDefault).UpdateStatus(c); err != nil {
		return err
	}

	rvStore := getResourceVersionStore()
	rvStore.save(c)

	glog.Infof("Created cluster")

	if err := d.copyKubeConfig(master); err != nil {
  	return fmt.Errorf("unable to write kubeconfig: %v", err)
  }

  oldKubeConfig := os.Getenv("KUBECONFIG")
  newKubeConfig := "kubeconfig"
  glog.Infof("Pointing KUBECONFIG to new cluster with config %s", newKubeConfig)
  os.Setenv("KUBECONFIG", newKubeConfig)
  defer func () {
    glog.Infof("Restoring KUBECONFIG to %s", oldKubeConfig)
    os.Setenv("KUBECONFIG", oldKubeConfig)
  }()

  // wait till apisever ready.
  // On the new cluster, apply the apiserver.
  glog.Infof("Provisioned Cluster - Applying Cluster API apiserver")
	apiserverErr := util.Retry(func() (bool, error) {
		glog.Infof("Waiting for Kubernetes to come up...")
		err := d.machineDeployer.CreateAPIServer()
		if err != nil {
			glog.Errorf("Error while applying %s", err)
			return false, nil
		}
		glog.Infof("Applied.")
		return true, nil
	}, 5)

	if apiserverErr != nil {
		return fmt.Errorf("timedout applying CLuster API apiserver: %s", apiserverErr)
	}

  provisionedClientSet, err := util.NewClientSet(newKubeConfig)
  if err != nil {
		return fmt.Errorf("Could not create client: %s", err)
  }
  // Pivot objects
  if err = pivot(bootstrapClientSet,  provisionedClientSet, rvStore); err != nil {
    return fmt.Errorf("Pivot error: %s", err)
  }

  glog.Infof("Provisioned Cluster - Applying machine controllers")
  if err := d.machineDeployer.CreateControllers(c, machines); err != nil {
  	return fmt.Errorf("can't create machine controller: %v", err)
  }

	glog.Infof("Starting node machine creation")

	if err := d.createMachines(machines); err != nil {
		return err
	}

	glog.Infof("Created node machines")

  glog.Infof("Stopping local minikube")
  glog.Infof(util.ExecCommand("./minikube","delete"))

  // TODO: switch kubeconfig to be the the one pointing at new cluster.

	return nil
}

type resourceVersionGetter interface {
	getResourceVersion(uid types.UID) string
}

type metaGetter interface {
  GetObjectMeta() metav1.Object
}

type resourceVersionStore struct {
  m map[types.UID]string
}

func getResourceVersionStore() *resourceVersionStore {
  return &resourceVersionStore{m: make(map[types.UID]string)}
}

func (s resourceVersionStore) getResourceVersion (uid types.UID) string {
  v,ok := s.m[uid]
  glog.Infof("UID %v found: %v returned %v", uid, ok, v)
  return v
}

func (s resourceVersionStore) save(g metaGetter) {
  s.m[g.GetObjectMeta().GetUID()] = g.GetObjectMeta().GetResourceVersion()
  glog.Infof("UID %v saved: %v", g.GetObjectMeta().GetUID(), g.GetObjectMeta().GetResourceVersion())
}


func pivot(from, to *clientset.Clientset, rvg resourceVersionGetter) error {
  if err := waitForClusterResourceReady(from); err != nil {
    return fmt.Errorf("Cluster resource not ready on source cluster.")
  }

  if err := waitForClusterResourceReady(to); err != nil {
    return fmt.Errorf("Cluster resource not ready on target cluster.")
  }

  // TODO: Iterate over all namespaces we could have Cluster API Objects
  // TODO: As we move objects, update any references (eg. owner ref) in following object to new UID
  clusters, err := from.ClusterV1alpha1().Clusters(apiv1.NamespaceDefault).List(metav1.ListOptions{})
  if err != nil {
    return err
  }

  for _, cluster := range clusters.Items {
      rv := rvg.getResourceVersion(cluster.UID)
      cluster, err := from.ClusterV1alpha1().Clusters(apiv1.NamespaceDefault).Get(cluster.Name, metav1.GetOptions{ResourceVersion:rv})
      if err != nil {
          return err
      }
      cluster.SetResourceVersion("")
      _, err = to.ClusterV1alpha1().Clusters(apiv1.NamespaceDefault).Create(cluster)
      if err != nil {
          return err
      }
      glog.Infof("Moved Cluster '%s' rev '%s'", cluster.GetName(), rv)
  }

    machines, err := from.ClusterV1alpha1().Machines(apiv1.NamespaceDefault).List(metav1.ListOptions{})
    if err != nil {
      return err
    }

    for _, machine := range machines.Items {
        rv := rvg.getResourceVersion(machine.UID)
        if err != nil {
          return err
        }
        machine, err := from.ClusterV1alpha1().Machines(apiv1.NamespaceDefault).Get(machine.Name, metav1.GetOptions{ResourceVersion:rv})
        machine.SetResourceVersion("")
        _, err = to.ClusterV1alpha1().Machines(apiv1.NamespaceDefault).Create(machine)
        if err != nil {
            return err
        }
        glog.Infof("Moved Machine '%s' rev '%s'", machine.GetName(), rv)
    }
    return nil
}


/*

  // Note hostPort has all sorts of CNI incompatabilities, therefore using nodePort. Loadbalancer feels a little heavy handed for temporary bootstrapping access but is an option.
  // TODO: switch etcd service back to ClusterIP from nodePort to lock it back down.
  // Pivot data
  // Don't forget to actually open firewall:
  // gcloud compute firewall-rules create cluster-api-etcd-open --allow=TCP:32379 --source-ranges=0.0.0.0/0 --target-tags='https-server'
  glog.Infof("Wating for ETCD availability")
  waitErr := util.Retry(func() (bool, error) {
  		glog.Infof("Waiting...")
  		_, err :=  exec.Command("nc","-z", masterIP, "32379").Output()
  		return (err == nil), nil
  	}, 5)

  	if waitErr != nil {
  		return fmt.Errorf("timedout waiting for ETCD to come online at %s:%s", masterIP, "32379")
  	}

  glog.Infof("Pivoting Data")
  os.Setenv("KUBECONFIG", oldKubeConfig)
  glog.Infof(util.ExecCommand("kubectl", "version"))
  out, err :=  exec.Command("kubectl","exec", "etcd-clusterapi-0", "--", "etcdctl","get '/registry/k8s.io/cluster.k8s.io' --prefix=true --keys-only").CombinedOutput();
  if err != nil  {
  		return fmt.Errorf("could not get current ETCD data keys %v", err)
  }
  var etcdKeyCount int64
  scanner := bufio.NewScanner(strings.NewReader(string(out)))
  for scanner.Scan() {
      line := scanner.Text()
      if line != "" {
            glog.Infof("ETCD Key: %s", line)
            etcdKeyCount++
      }
  }
  if etcdKeyCount == 0 {
    return  fmt.Errorf("could not parse any ETCD data keys. Output: %v", string(out))
  }

  pivotCmd :=  exec.Command("kubectl","exec", "etcd-clusterapi-0","--", "etcdctl", fmt.Sprintf("make-mirror --prefix='/registry/k8s.io/cluster.k8s.io' %s:32379", masterIP));
	pivotReader, _ := pivotCmd.StdoutPipe()
	pivotScanner := bufio.NewScanner(pivotReader)
	go func(){
	  defer func () {
        glog.Info("Ending pivot")
        if pivotCmd.Process == nil {
          glog.Error("Pivot process was nil. Cannot kill.")
        }
        pivotCmd.Process.Kill()
	  } ()
	  for pivotScanner.Scan() {
        line := pivotScanner.Text()
        count, _ := strconv.ParseInt(line, 10, 64)
        glog.Info("Pivoted %d keys", count)
        if count == etcdKeyCount {
          return
        }
    }
	}()

	_ = pivotCmd.Run()
*/

/*
	glog.Infof(util.ExecCommand("./minikube","ssh","--","sudo","cp", "/var/lib/localkube/kubeconfig","/etc/kubernetes/admin.conf"))
	// TODO: sanity check if these is the right spot for apiserver.crt file
	glog.Infof(util.ExecCommand("./minikube","ssh","--","sudo","cp", "/var/lib/localkube/certs/*", "/etc/ssl/certs"))
	glog.Infof(util.ExecCommand("./minikube","ssh","--","sudo","sed", "-i","'s=/var/lib/localkube/certs=/etc/ssl/certs=g'","/etc/kubernetes/admin.conf"))
	out,_ :=  exec.Command("./minikube", "ip").Output()
	ip := strings.TrimRight(string(out), "\r\n")
  glog.Info("minikube ip: %s", ip)
	glog.Infof(util.ExecCommand("./minikube","ssh","--","sudo","sed", "-i", fmt.Sprintf("'s=localhost:8443=%s:8443=g'", ip),"/etc/kubernetes/admin.conf"))

	// figure out why this hates me later.
	glog.Infof(util.ExecCommand("kubectl","label", "node", "minikube", "node-role.kubernetes.io/master=\"\""))
*/

/*
func kubeExec(namespace, pod, container, command, args string) (Writer, Writer, error) {
   	execRequest := kubeClient.CoreV1().RESTClient().Post().
   		Resource("pods").
   		Name(pod).
   		Namespace(namespace).
   		SubResource("exec").
   		Param("container", container).
   		Param("command", req.Command).
   		Param("stdin", "true").
   		Param("stdout", "false").
   		Param("stderr", "false").
   		Param("tty", "false")

   	exec, err := remotecommand.NewSPDYExecutor(config, "POST", execRequest.URL())
   	if err != nil {
   		return icinga.UNKNOWN, err
   	}

   	stdIn := newStringReader([]string{"-c", req.Arg})
   	stdOut := new(Writer)
   	stdErr := new(Writer)

   	err = exec.Stream(remotecommand.StreamOptions{
   		Stdin:  stdIn,
   		Stdout: stdOut,
   		Stderr: stdErr,
   		Tty:    false,
   	})
}
*/

func (d *deployer) waitForClusterResourceReady() error {
	return waitForClusterResourceReady(d.clientSet)
}

func waitForClusterResourceReady(cs clientset.Interface) error {
	err := util.Poll(500*time.Millisecond, 120*time.Second, func() (bool, error) {
		_, err := cs.Discovery().ServerResourcesForGroupVersion("cluster.k8s.io/v1alpha1")
		if err == nil {
			return true, nil
		}
		return false, nil
	})

	return err
}

func (d *deployer) createMachines(machines []*clusterv1.Machine) error {
  var err error
	for _, machine := range machines {
		for i := 0; i < 10; i++ {
  		var m *clusterv1.Machine
  		m, err = d.client.Machines(apiv1.NamespaceDefault).Create(machine)
  		if err != nil {
  			glog.Info("Hanging to create machine [%s]: %v", m.Name, err)
  			time.Sleep(time.Duration(SleepSecondsPerAttempt) * time.Second)
  			continue
  		}
  		glog.Infof("Added machine [%s]", m.Name)
  		break
  	}
	}
	return err
}

func (d *deployer) createMachine(m *clusterv1.Machine) error {
	return d.createMachines([]*clusterv1.Machine{m})
}

func (d *deployer) deleteAllMachines() error {
	machines, err := d.client.Machines(apiv1.NamespaceDefault).List(metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, m := range machines.Items {
		if !util.IsMaster(&m) {
			if err := d.delete(m.Name); err != nil {
				return err
			}
			glog.Infof("Deleted machine object %s", m.Name)
		}
	}
	return nil
}

func (d *deployer) delete(name string) error {
	err := d.client.Machines(apiv1.NamespaceDefault).Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	err = util.Poll(500*time.Millisecond, 120*time.Second, func() (bool, error) {
		if _, err = d.client.Machines(apiv1.NamespaceDefault).Get(name, metav1.GetOptions{}); err == nil {
			return false, nil
		}
		return true, nil
	})
	return err
}

func (d *deployer) listMachines() ([]*clusterv1.Machine, error) {
	machines, err := d.client.Machines(apiv1.NamespaceDefault).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return util.MachineP(machines.Items), nil
}

func (d *deployer) getCluster() (*clusterv1.Cluster, error) {
	clusters, err := d.client.Clusters(apiv1.NamespaceDefault).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	if len(clusters.Items) != 1 {
		return nil, fmt.Errorf("cluster object count != 1")
	}
	return &clusters.Items[0], nil
}

func (d *deployer) getMasterIP(master *clusterv1.Machine) (string, error) {
	for i := 0; i < MasterIPAttempts; i++ {
		ip, err := d.machineDeployer.GetIP(master)
		if err != nil || ip == "" {
			glog.Info("Hanging for master IP...")
			time.Sleep(time.Duration(SleepSecondsPerAttempt) * time.Second)
			continue
		}
		return ip, nil
	}
	return "", fmt.Errorf("unable to find Master IP after defined wait")
}

func (d *deployer) copyKubeConfig(master *clusterv1.Machine) error {
	writeErr := util.Retry(func() (bool, error) {
		glog.Infof("Waiting for Kubernetes to come up...")
		config, err := d.machineDeployer.GetKubeConfig(master)
		if err != nil {
			glog.Errorf("Error while retriving kubeconfig %s", err)
			return false, nil
		}
		if config == "" {
			return false, nil
		}
		glog.Infof("Kubernetes is up.. Writing kubeconfig to disk.")
		err = d.writeConfigToDisk(config)
		return (err == nil), nil
	}, 5)

	if writeErr != nil {
		return fmt.Errorf("timedout writing kubeconfig: %s", writeErr)
	}
	return nil
}

func (d *deployer) initApiClient() error {
	c, err := util.NewClientSet(d.configPath)
	if err != nil {
		return err
	}
	d.clientSet = c
	d.client = c.ClusterV1alpha1()
	return nil
}

func (d *deployer) writeConfigToDisk(config string) error {
	file, err := os.Create("kubeconfig")
	if err != nil {
		return err
	}
	if _, err := file.WriteString(config); err != nil {
		return err
	}
	defer file.Close()

	file.Sync() // flush
	glog.Infof("wrote kubeconfig to [%s]", d.configPath)
	return nil
}

// Make sure you successfully call setMasterIp first.
func (d *deployer) waitForApiserver(master string) error {
	endpoint := fmt.Sprintf("https://%s/healthz", master)

	// Skip certificate validation since we're only looking for signs of
	// health, and we're not going to have the CA in our default chain.
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	waitErr := util.Retry(func() (bool, error) {
		glog.Info("Waiting for apiserver to become healthy...")
		resp, err := client.Get(endpoint)
		return (err == nil && resp.StatusCode == 200), nil
	}, 3)

	if waitErr != nil {
		glog.Errorf("Error waiting for apiserver: %s", waitErr)
		return waitErr
	}
	return nil
}

// Make sure the default service account in kube-system namespace exists.
func (d *deployer) waitForServiceAccount() error {
	client, err := util.NewKubernetesClient(d.configPath)
	if err != nil {
		return err
	}

	waitErr := util.Retry(func() (bool, error) {
		glog.Info("Waiting for the service account to exist...")
		_, err = client.CoreV1().ServiceAccounts(ServiceAccountNs).Get(ServiceAccountName, metav1.GetOptions{})
		return (err == nil), nil
	}, 5)

	if waitErr != nil {
		glog.Errorf("Error waiting for service account: %s", waitErr)
		return waitErr
	}
	return nil
}
