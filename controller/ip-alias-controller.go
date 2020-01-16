package controller

import (
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type IPAliasController struct {
	kubeClient            kubernetes.Interface
	endpointsLister       corelisters.EndpointsLister
	endpointsListerSynced cache.InformerSynced
	nodesLister           corelisters.NodeLister
	nodesListerSynced     cache.InformerSynced
	serviceLister         corelisters.ServiceLister
	serviceListerSynced   cache.InformerSynced
	queue                 workqueue.RateLimitingInterface
	serviceQueue          workqueue.RateLimitingInterface
	cache                 map[string]*Resource
	sync.RWMutex
}

type Resource struct {
	Name      string
	Kind      string
	Namespace string
}

func NewIPAliasController(
	endpointInformer coreinformers.EndpointsInformer,
	serviceInformer coreinformers.ServiceInformer,
	nodeInformer coreinformers.NodeInformer,
) *IPAliasController {

	IPAliasController := &IPAliasController{
		queue:        workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ep"),
		serviceQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "svc"),
		// TODO: use expire cache, delete unused cache
		cache: make(map[string]*Resource, 0),
	}

	endpointInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				IPAliasController.enqueueEndpoint(obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				IPAliasController.enqueueEndpoint(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				IPAliasController.enqueueEndpoint(obj)
			},
		},
		0*time.Minute,
	)

	serviceInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				IPAliasController.enqueueService(obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				IPAliasController.enqueueService(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				IPAliasController.enqueueService(obj)
			},
		},
		0*time.Minute,
	)

	IPAliasController.endpointsLister = endpointInformer.Lister()
	IPAliasController.endpointsListerSynced = endpointInformer.Informer().HasSynced
	IPAliasController.serviceLister = serviceInformer.Lister()
	IPAliasController.serviceListerSynced = serviceInformer.Informer().HasSynced
	IPAliasController.nodesLister = nodeInformer.Lister()
	IPAliasController.nodesListerSynced = nodeInformer.Informer().HasSynced

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
func (o *IPAliasController) enqueueService(obj interface{}) {
	glog.Infof("enqueue Service %v", obj)
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %+v: %v", obj, err))
		return
	}

	o.serviceQueue.Add(key)
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (o *IPAliasController) runWorker(queue workqueue.RateLimitingInterface, syncer func(string) error) func() {
	return func() {
		for o.processNextWorkItem(queue, syncer) {
		}
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (o *IPAliasController) processNextWorkItem(queue workqueue.RateLimitingInterface, syncer func(string) error) bool {
	obj, shutdown := queue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer queue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			queue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Dataset resource to be synced.
		if err := syncer(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			queue.AddRateLimited(key)
			glog.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		queue.Forget(obj)
		glog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

func (o *IPAliasController) syncService(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	svc, err := o.serviceLister.Services(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if svc.Spec.ClusterIP != "" {
		o.Set(svc.Spec.ClusterIP, &Resource{
			Kind:      "Service",
			Name:      svc.Name,
			Namespace: svc.Namespace,
		})
	}

	return nil
}

func (o *IPAliasController) syncEndpoint(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	ep, err := o.endpointsLister.Endpoints(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	for _, subset := range ep.Subsets {
		for _, address := range subset.Addresses {
			// seems not work, how clean stale ip? ip will re-allocate? need clean?
			// if ep.GetDeletionTimestamp() != nil {
			// 	o.Delete(address.IP)
			// } else {
			hostNetwork := false
			if address.NodeName != nil {
				node, err := o.nodesLister.Get(*address.NodeName)
				if err != nil {
					return err
				}
				if node.Status.Addresses[0].Address == address.IP {
					o.Set(address.IP, &Resource{
						Kind: "Node",
						Name: *address.NodeName,
					})
					hostNetwork = true
				}
			}
			if address.TargetRef != nil && !hostNetwork {
				o.Set(address.IP, &Resource{
					Kind:      address.TargetRef.Kind,
					Name:      address.TargetRef.Name,
					Namespace: address.TargetRef.Namespace,
				})
			}
			// }
		}
	}

	return nil
}

func (o *IPAliasController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer o.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh,
		o.endpointsListerSynced,
		o.serviceListerSynced,
		o.nodesListerSynced,
	) {
		return
	}
	for i := 0; i < workers; i++ {
		go wait.Until(o.runWorker(o.queue, o.syncEndpoint), time.Second, stopCh)
		go wait.Until(o.runWorker(o.serviceQueue, o.syncService), time.Second, stopCh)
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
