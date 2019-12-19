package controller

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"

	"github.com/alauda/captain/pkg/helm"

	alpha1 "github.com/alauda/helm-crds/pkg/apis/app/v1alpha1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

// newChartRepoHandler create new event handler for ChartRepo
// Because we only need to watch ChartRepos from one namespace, for now we will
// use a dumb filter...
func (c *Controller) newChartRepoHandler() cache.ResourceEventHandler {
	updateFunc := func(old, new interface{}) {
		// oldChartRepo := old.(*alpha1.ChartRepo)
		// newChartRepo := new.(*alpha1.ChartRepo)
		klog.V(4).Info("receive new chartrepo: ", old.(*alpha1.ChartRepo), new.(*alpha1.ChartRepo))
		n, ok := new.(*alpha1.ChartRepo)
		if ok {
			now := v1.Now()
			if n.GetAnnotations() != nil && n.GetAnnotations() []{
			}
		}

		c.enqueueChartRepo(new)

	}

	deleteFunc := func(obj interface{}) {
		cr, ok := obj.(*alpha1.ChartRepo)
		if ok {
			if err := helm.RemoveRepository(cr.GetName()); err != nil {
				klog.Error("remove repo from helm error:", err)
			}
			klog.Info("delete chartrepo: ", cr.GetName())
		}
	}

	funcs := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueChartRepo,
		UpdateFunc: updateFunc,
		DeleteFunc: deleteFunc,
	}

	return funcs
}

// newHelmRequestHandler create an HelmRequest handler
// The cache package have a built-in filter-support handler, but it cannot compare the old and new
// obj at the same time
func (c *Controller) newHelmRequestHandler() cache.ResourceEventHandler {
	updateFunc := func(old, new interface{}) {
		oldHR := old.(*alpha1.HelmRequest)
		newHR := new.(*alpha1.HelmRequest)
		// this is a bit of tricky
		// 1. old and new -> 1 cluster => check version and spec
		// 2. old and new -> N cluster => no check
		// 3. old 1 / new N => spec and version changed
		// 4. old N / new 1 => spec and version changed

		if oldHR.Spec.InstallToAllClusters && newHR.Spec.InstallToAllClusters {
			c.enqueueHelmRequest(new)
		} else {

			if newHR.DeletionTimestamp != nil {
				klog.V(4).Infof("get an helmrequest with deletiontimestap: %s", newHR.Name)
				c.enqueueHelmRequest(new)
			}

			if newHR.Status.Phase == alpha1.HelmRequestPending {
				klog.V(4).Infof("")
			}

			if oldHR.ResourceVersion == newHR.ResourceVersion {
				return
			}
			if reflect.DeepEqual(oldHR.Spec, newHR.Spec) {
				klog.V(4).Infof("spec equal, not update: %s", newHR.Name)
				return
			}
			klog.V(4).Infof("old hr: %+v, new hr: %+v", oldHR.Spec, newHR.Spec)
			c.enqueueHelmRequest(new)
		}
	}

	funcs := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueHelmRequest,
		UpdateFunc: updateFunc,
		DeleteFunc: c.deleteHandler,
	}

	return funcs
}

// newClusterHelmRequestHandler create an HelmRequest handler
// The cache package have a built-in filter-support handler, but it cannot compare the old and new
// obj at the same time
func (c *Controller) newClusterHelmRequestHandler(name string) cache.ResourceEventHandler {
	updateFunc := func(old, new interface{}) {
		oldHR := old.(*alpha1.HelmRequest)
		newHR := new.(*alpha1.HelmRequest)
		// this is a bit of tricky
		// 1. old and new -> 1 cluster => check version and spec
		// 2. old and new -> N cluster => no check
		// 3. old 1 / new N => spec and version changed
		// 4. old N / new 1 => spec and version changed

		if oldHR.Spec.InstallToAllClusters && newHR.Spec.InstallToAllClusters {
			c.enqueueClusterHelmRequest(new, name)
		} else {

			if newHR.DeletionTimestamp != nil {
				klog.V(4).Infof("get an helmrequest with deletiontimestap: %s", newHR.Name)
				c.enqueueClusterHelmRequest(new, name)
			}

			if newHR.Status.Phase == alpha1.HelmRequestPending {
				klog.V(4).Infof("")
			}

			if oldHR.ResourceVersion == newHR.ResourceVersion {
				return
			}
			if reflect.DeepEqual(oldHR.Spec, newHR.Spec) {
				klog.V(4).Infof("spec equal, not update: %s", newHR.Name)
				return
			}
			klog.V(4).Infof("old hr: %+v, new hr: %+v", oldHR.Spec, newHR.Spec)
			c.enqueueClusterHelmRequest(new, name)
		}
	}

	addFunc := func(obj interface{}) {
		klog.Infof("receive hr create event: %+v", obj)
		c.enqueueClusterHelmRequest(obj, name)
	}

	deleteFunc := func(obj interface{}) {
		c.deleteClusterHelmRequestHandler(obj, name)
	}

	funcs := cache.ResourceEventHandlerFuncs{
		AddFunc:    addFunc,
		UpdateFunc: updateFunc,
		DeleteFunc: deleteFunc,
	}

	return funcs
}
