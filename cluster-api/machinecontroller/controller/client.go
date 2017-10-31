package controller

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	machinesv1 "k8s.io/kube-deploy/cluster-api/api/machines/v1alpha1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiv1 "k8s.io/api/core/v1"
)

func restClient(kubeconfigpath string) (*rest.RESTClient, *runtime.Scheme, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigpath)
	if err != nil {
		return nil, nil, err
	}

	scheme := runtime.NewScheme()
	if err := machinesv1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}

	config := *cfg
	config.GroupVersion = &machinesv1.SchemeGroupVersion
	config.APIPath = "/apis"
	config.ContentType = runtime.ContentTypeJSON
	config.NegotiatedSerializer = serializer.DirectCodecFactory{CodecFactory: serializer.NewCodecFactory(scheme)}

	client, err := rest.RESTClientFor(&config)
	if err != nil {
		return nil, nil, err
	}

	return client, scheme, nil
}

// TODO: Replace bellow with the proper CRUD client
func newCrdClient(cl *rest.RESTClient, scheme *runtime.Scheme) *crdclient {
	return &crdclient{cl: cl, codec: runtime.NewParameterCodec(scheme)}
}

type crdclient struct {
	cl     *rest.RESTClient
	codec  runtime.ParameterCodec
}

func (f *crdclient) List(opts meta_v1.ListOptions) (*machinesv1.MachineList, error) {
	var result machinesv1.MachineList
	err := f.cl.Get().
		Namespace(apiv1.NamespaceAll).Resource(machinesv1.MachinesCRDPlural).
		VersionedParams(&opts, f.codec).
		Do().Into(&result)
	return &result, err
}
