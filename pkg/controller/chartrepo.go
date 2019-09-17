package controller

import (
	"fmt"
	"github.com/Jeffail/gabs/v2"
	"github.com/alauda/captain/pkg/helm"
	"github.com/alauda/captain/pkg/util"
	"github.com/alauda/helm-crds/pkg/apis/app/v1alpha1"
	"helm.sh/helm/pkg/repo"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
	"strings"
)

// updateChartRepoStatus update ChartRepo's status
func (c *Controller) updateChartRepoStatus(cr *v1alpha1.ChartRepo, phase v1alpha1.ChartRepoPhase, reason string) {
	//cr = cr.DeepCopy()
	//cr.Status.Phase = phase
	//cr.Status.Reason = reason

	data := gabs.New()
	data.SetP(phase, "status.phase")
	data.SetP(reason, "status.reason")

	_, err := c.appClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Patch(
		cr.GetName(),
		types.MergePatchType,
		data.Bytes(),
	)

	if err != nil {
		klog.Error("update chartrepo error: ", err)
	}

	//_, err := c.appClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Update(cr)
	//if err != nil {
	//	if apierrors.IsConflict(err) {
	//		klog.Warningf("chartrepo %s update conflict, rerty... ", cr.GetName())
	//		old, err := c.appClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Get(cr.GetName(), v1.GetOptions{})
	//		if err != nil {
	//			klog.Error("chartrepo update-get error:", err)
	//		} else {
	//			cr.ResourceVersion = old.ResourceVersion
	//			_, err := c.appClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Update(cr)
	//			if err != nil {
	//				klog.Error("chartrepo update-update error:", err)
	//			}
	//		}
	//	} else {
	//		klog.Error("update chartrepo error: ", err)
	//	}
	//}
}

// syncChartRepo sync ChartRepo to helm repo store
func (c *Controller) syncChartRepo(obj interface{}) {

	cr := obj.(*v1alpha1.ChartRepo)

	var username string
	var password string

	if cr.Spec.Secret != nil {
		ns := cr.Spec.Secret.Namespace
		if ns == "" {
			ns = cr.Namespace
		}
		secret, err := c.kubeClient.CoreV1().Secrets(ns).Get(cr.Spec.Secret.Name, v1.GetOptions{})
		if err != nil {
			c.updateChartRepoStatus(cr, v1alpha1.ChartRepoFailed, err.Error())
			klog.Error("get secret for chartrepo error: ", err)
			return
		}
		data := secret.Data
		username = string(data["username"])
		password = string(data["password"])

	}

	if err := helm.AddBasicAuthRepository(cr.GetName(), cr.Spec.URL, username, password); err != nil {
		c.updateChartRepoStatus(cr, v1alpha1.ChartRepoFailed, err.Error())
		return
	}

	if err := c.createCharts(cr); err != nil {
		c.updateChartRepoStatus(cr, v1alpha1.ChartRepoFailed, err.Error())
		return
	}

	c.updateChartRepoStatus(cr, v1alpha1.ChartRepoSynced, "")
	klog.Info("synced chartrepo: ", cr.GetName())
	return

}

// createCharts create charts resource for a repo
func (c *Controller) createCharts(cr *v1alpha1.ChartRepo) error {
	index, err := helm.GetChartsForRepo(cr.GetName())
	if err != nil {
		return err
	}

	options := v1.GetOptions{}
	for name, versions := range index.Entries {
		chart := generateChartResource(versions, name, cr)
		old, err := c.appClientSet.AppV1alpha1().Charts(cr.GetNamespace()).Get(getChartName(cr.GetName(), name), options)
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.Infof("chart %s/%s not found, create", cr.GetName(), name)
				_, err = c.appClientSet.AppV1alpha1().Charts(cr.GetNamespace()).Create(chart)
				if err != nil {
					return err
				}
				continue
			} else {
				return err
			}
		}

		if compareChart(old, chart) {
			chart.SetResourceVersion(old.GetResourceVersion())
			_, err = c.appClientSet.AppV1alpha1().Charts(cr.GetNamespace()).Update(chart)
			if err != nil {
				return err
			}
		}

	}
	return nil

}

// compareChart simply compare versions list length
func compareChart(old *v1alpha1.Chart, new *v1alpha1.Chart) bool {
	if len(old.Spec.Versions) != len(new.Spec.Versions) {
		return true
	}
	return false
}

func getChartName(repo, chart string) string {
	return fmt.Sprintf("%s.%s", strings.ToLower(chart), repo)
}

// generateChartResource create a Chart resource from the information in helm cache index
func generateChartResource(versions repo.ChartVersions, name string, cr *v1alpha1.ChartRepo) *v1alpha1.Chart {

	var vs []*v1alpha1.ChartVersion
	for _, v := range versions {
		vs = append(vs, &v1alpha1.ChartVersion{ChartVersion: *v})
	}

	spec := v1alpha1.ChartSpec{
		Versions: vs,
	}

	labels := map[string]string{
		"repo": cr.GetName(),
	}
	if cr.GetLabels() != nil {
		project := cr.GetLabels()[util.ProjectKey]
		if project != "" {
			labels[util.ProjectKey] = project
		}
	}

	chart := v1alpha1.Chart{
		TypeMeta: v1.TypeMeta{
			Kind:       "Chart",
			APIVersion: "app.alauda.io/v1alpha1",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      getChartName(cr.GetName(), name),
			Namespace: cr.GetNamespace(),
			Labels:    labels,
		},
		Spec: spec,
	}

	return &chart

}

func (c *Controller) runChartRepoWorker() {
	for c.processNextChartRepo() {
	}
}

// processNextWorkItem will read a single work item off the chartRepoWorkQueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextChartRepo() bool {
	obj, shutdown := c.chartRepoWorkQueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.chartRepoWorkQueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the chartRepoWorkQueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the chartRepoWorkQueue and attempted again after a back-off
		// period.
		defer c.chartRepoWorkQueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the chartRepoWorkQueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// chartRepoWorkQueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// chartRepoWorkQueue.
		if key, ok = obj.(string); !ok {
			// As the item in the chartRepoWorkQueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.chartRepoWorkQueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in chartRepoWorkQueue but got %#v", obj))
			return nil
		}
		// Start the syncHandler, passing it the namespace/name string of the
		// HelmRequest resource to be synced.
		if err := c.syncChartRepoHandler(key); err != nil {
			// Put the item back on the chartRepoWorkQueue to handle any transient errors.
			c.chartRepoWorkQueue.AddRateLimited(key)
			return fmt.Errorf("error syncing chartrepo '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.chartRepoWorkQueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the HelmRequest resource
// with the current status of the resource.
func (c *Controller) syncChartRepoHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}
	klog.V(9).Infof("")

	// Get the HelmRequest resource with this namespace/name
	chartRepo, err := c.chartRepoLister.ChartRepos(namespace).Get(name)
	if err != nil {
		// The HelmRequest resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("repo '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	if !chartRepo.DeletionTimestamp.IsZero() {
		klog.Infof("HelmRequest has not nil DeletionTimestamp, starting to delete it: %s", chartRepo.Name)
		return nil
	}

	c.syncChartRepo(chartRepo)

	return nil
}


// enqueueHelmRequest takes a HelmRequest resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than HelmRequest.
func (c *Controller) enqueueChartRepo(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.chartRepoWorkQueue.Add(key)
}