package controller

import (
	"fmt"
	"strings"

	"github.com/Jeffail/gabs/v2"
	"github.com/alauda/captain/pkg/helm"
	"github.com/alauda/captain/pkg/util"
	"github.com/alauda/helm-crds/pkg/apis/app/v1alpha1"
	"helm.sh/helm/pkg/repo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
)

// updateChartRepoStatus update ChartRepo's status
func (c *Controller) updateChartRepoStatus(cr *v1alpha1.ChartRepo, phase v1alpha1.ChartRepoPhase, reason string) {
	//cr = cr.DeepCopy()
	//cr.Status.Phase = phase
	//cr.Status.Reason = reason

	data := gabs.New()
	data.SetP(phase, "status.phase")
	data.SetP(reason, "status.reason")
	if phase == v1alpha1.ChartRepoSynced {
		now, _ := v1.Now().MarshalQueryParameter()
		k := c.GetConfig().LabelBaseDomain + "/last-sync-at"
		data.Set(now, "metadata", "annotations", k)
	}

	_, err := c.appClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Patch(
		cr.GetName(),
		types.MergePatchType,
		data.Bytes(),
	)

	if err != nil {
		klog.Error("update chartrepo error: ", err)
	}
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
	return

}

// createCharts create charts resource for a repo
// TODO: ace.ACE
func (c *Controller) createCharts(cr *v1alpha1.ChartRepo) error {
	checked := map[string]bool{}
	index, err := helm.GetChartsForRepo(cr.GetName())
	if err != nil {
		return err
	}
	// this may causes bugs
	for name, _ := range index.Entries {
		checked[strings.ToLower(name)] = true
	}

	existCharts := map[string]v1alpha1.Chart{}
	listOptions := v1.ListOptions{
		LabelSelector: fmt.Sprintf("repo=%s", cr.GetName()),
	}
	charts, err := c.appClientSet.AppV1alpha1().Charts(cr.GetNamespace()).List(listOptions)
	if err != nil {
		return err
	}
	for _, item := range charts.Items {
		name := strings.Split(item.GetName(), ".")[0]
		existCharts[name] = item
	}

	for on, versions := range index.Entries {
		name := strings.ToLower(on)
		chart := generateChartResource(versions, name, cr)
		// chart name can be uppercase in helm
		if _, ok := existCharts[name]; !ok {
			klog.Infof("chart %s/%s not found, create", cr.GetName(), on)
			_, err = c.appClientSet.AppV1alpha1().Charts(cr.GetNamespace()).Create(chart)
			if err != nil {
				if !apierrors.IsAlreadyExists(err) {
					return err
				}
			}
			continue
		}

		old := existCharts[name]

		if compareChart(old, chart) {
			chart.SetResourceVersion(old.GetResourceVersion())
			_, err = c.appClientSet.AppV1alpha1().Charts(cr.GetNamespace()).Update(chart)
			if err != nil {
				return err
			}
		}

	}

	for name, item := range existCharts {
		if !checked[name] {
			err := c.appClientSet.AppV1alpha1().Charts(cr.GetNamespace()).Delete(item.GetName(), &v1.DeleteOptions{})
			if err != nil {
				return err
			}
			klog.Info("delete charts: ", item.GetName())
		}

	}

	return nil

}

// compareChart simply compare versions list length
func compareChart(old v1alpha1.Chart, new *v1alpha1.Chart) bool {
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
			OwnerReferences: []v1.OwnerReference{
				*util.NewOwnerRef(cr, schema.GroupVersionKind{
					Group:   "app.alauda.io",
					Version: "v1alpha1",
					Kind:    "ChartRepo",
				}),
			},
		},
		Spec: spec,
	}

	return &chart

}
