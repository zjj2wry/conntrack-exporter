package controller

import (
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type IPAliasController struct {
	kubeClient   kubernetes.Interface
	lister       corelisters.EndpointsLister
	listerSynced cache.InformerSynced
	queue        workqueue.RateLimitingInterface
	cache        map[string]*Resource
	sync.RWMutex
}

type Resource struct {
	Name      string
	Kind      string
	Namespace string
}

func NewIPAliasController(
	endpointInformer coreinformers.EndpointsInformer,
) *IPAliasController {

	IPAliasController := &IPAliasController{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "IPAlias"),
	}

	// configure the namespace informer event handlers
	endpointInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				IPAliasController.enqueueEndpoint(obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				IPAliasController.enqueueEndpoint(newObj)
			},
		},
		0*time.Minute,
	)

	IPAliasController.lister = endpointInformer.Lister()
	IPAliasController.listerSynced = endpointInformer.Informer().HasSynced

	return IPAliasController
}

func (o *IPAliasController) enqueueEndpoint(obj interface{}) {
	glog.Infof("enqueue Endpoint %v", obj)
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %+v: %v", obj, err))
		return
	}

	o.queue.Add(key)
}

func (o *IPAliasController) enqueueEndpointAfter(obj interface{}, t int64) {
	glog.Infof("enqueue Endpoint %v", obj)
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %+v: %v", obj, err))
		return
	}

	o.queue.AddAfter(key, time.Duration(t)*time.Second)
}

func (o *IPAliasController) worker() {
	workFunc := func() bool {
		key, quit := o.queue.Get()
		if quit {
			return true
		}
		defer o.queue.Done(key)

		err := o.syncEndpoint(key.(string))
		if err == nil {
			o.queue.Forget(key)
			return false
		}

		if err != nil {
			o.queue.AddRateLimited(key)
			utilruntime.HandleError(err)
		}
		return false
	}

	for {
		quit := workFunc()

		if quit {
			return
		}
	}
}

func (o *IPAliasController) syncEndpoint(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	return nil
}

func (o *IPAliasController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer o.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh,
		o.listerSynced,
	) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(o.worker, time.Second, stopCh)
	}
	<-stopCh
}

func (o *IPAliasController) Get(key string) *Resource {
	o.RLock()
	value := o.cache[key]
	o.RUnlock()
	return value
}

func (o *IPAliasController) Set(key string, value *Resource) {
	o.Lock()
	o.cache[key] = value
	o.Unlock()
}

func (o *IPAliasController) Delete(key string) {
	o.Lock()
	delete(o.cache, key)
	o.Unlock()
}
