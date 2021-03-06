/*

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

package controllers

import (
	"context"
	"fmt"
	"github.com/alauda/captain/pkg/helm"
	"github.com/alauda/captain/pkg/util"
	"github.com/go-logr/logr"
	"helm.sh/helm/pkg/repo"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"strings"
	"time"

	alaudaiov1alpha1 "github.com/alauda/helm-crds/pkg/apis/app/v1alpha1"
)

// ChartRepoReconciler reconciles a ChartRepo object
type ChartRepoReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	// we only want to watch one namespace ,this is the easy way...
	Namespace string
}

// +kubebuilder:rbac:groups=alauda.io.alauda.io,resources=chartrepoes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=alauda.io.alauda.io,resources=chartrepoes/status,verbs=get;update;patch

func (r *ChartRepoReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("chartrepo", req.NamespacedName)

	if req.NamespacedName.Namespace != r.Namespace {
		return ctrl.Result{}, nil
	}

	// your logic here
	var cr alaudaiov1alpha1.ChartRepo
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		log.Error(err, "unable to fetch chartrepo")
		return ctrl.Result{}, ignoreNotFound(err)
	}

	if !r.isReadyForResync(&cr) {
		log.Info("not ready for resync")
		return ctrl.Result{}, nil
	}

	log.Info("resync chartrepo")

	if err := r.syncChartRepo(&cr, ctx); err != nil {
		log.Error(err, "sync chartrepo failed")
		return ctrl.Result{}, r.updateChartRepoStatus(ctx, &cr, alaudaiov1alpha1.ChartRepoFailed, err.Error())
	} else {
		return ctrl.Result{}, r.updateChartRepoStatus(ctx, &cr, alaudaiov1alpha1.ChartRepoSynced, "")
	}

}

func (r *ChartRepoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&alaudaiov1alpha1.ChartRepo{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 3}).
		Complete(r)
}

func isNotFound(err error) bool {
	return apierrs.IsNotFound(err)
}

func ignoreNotFound(err error) error {
	if isNotFound(err) {
		return nil
	}
	return err
}

// syncChartRepo sync ChartRepo to helm repo store
func (r *ChartRepoReconciler) syncChartRepo(cr *alaudaiov1alpha1.ChartRepo, ctx context.Context) error {
	// log := r.Log.WithValues("chartrepo", cr.GetName())

	var username string
	var password string

	if cr.Spec.Secret != nil {
		ns := cr.Spec.Secret.Namespace
		if ns == "" {
			ns = cr.Namespace
		}

		key := client.ObjectKey{Namespace: ns, Name: cr.Spec.Secret.Name}

		var secret corev1.Secret
		if err := r.Get(ctx, key, &secret); err != nil {
			return err
		}

		data := secret.Data
		username = string(data["username"])
		password = string(data["password"])

	}

	if err := helm.AddBasicAuthRepository(cr.GetName(), cr.Spec.URL, username, password); err != nil {
		return err
	}

	return r.createCharts(cr, ctx)

}

// createCharts create charts resource for a repo
// TODO: ace.ACE
func (r *ChartRepoReconciler) createCharts(cr *alaudaiov1alpha1.ChartRepo, ctx context.Context) error {
	log := r.Log.WithValues("chartrepo", cr.GetName())

	checked := map[string]bool{}
	index, err := helm.GetChartsForRepo(cr.GetName())
	if err != nil {
		return err
	}
	// this may causes bugs
	for name, _ := range index.Entries {
		checked[strings.ToLower(name)] = true
	}

	existCharts := map[string]alaudaiov1alpha1.Chart{}

	var charts alaudaiov1alpha1.ChartList
	labels := client.MatchingLabels{
		"repo": cr.GetName(),
	}

	if err := r.List(ctx, &charts, labels, client.InNamespace(r.Namespace)); err != nil {
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
			log.Info("chart not found, create", "name", cr.GetName()+"/"+on)
			err := r.Create(ctx, chart)
			if err != nil {
				if !apierrs.IsAlreadyExists(err) {
					return err
				}
			}
			continue
		}

		old := existCharts[name]

		if compareChart(old, chart) {
			chart.SetResourceVersion(old.GetResourceVersion())
			if err := r.Update(ctx, chart); err != nil {
				return err
			}
		}

	}

	for name, item := range existCharts {
		if !checked[name] {
			dc := item
			dc.SetNamespace(cr.GetNamespace())
			if err := r.Delete(ctx, &dc); err != nil {
				return err
			}
			log.Info("delete charts", "name", item.GetName())
		}

	}

	return nil

}

// compareChart simply compare versions list length
func compareChart(old alaudaiov1alpha1.Chart, new *alaudaiov1alpha1.Chart) bool {
	if len(old.Spec.Versions) != len(new.Spec.Versions) {
		return true
	}
	return false
}

func getChartName(repo, chart string) string {
	return fmt.Sprintf("%s.%s", strings.ToLower(chart), repo)
}

// generateChartResource create a Chart resource from the information in helm cache index
func generateChartResource(versions repo.ChartVersions, name string, cr *alaudaiov1alpha1.ChartRepo) *alaudaiov1alpha1.Chart {

	var vs []*alaudaiov1alpha1.ChartVersion
	for _, v := range versions {
		vs = append(vs, &alaudaiov1alpha1.ChartVersion{ChartVersion: *v})
	}

	spec := alaudaiov1alpha1.ChartSpec{
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

	chart := alaudaiov1alpha1.Chart{
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

// updateChartRepoStatus update ChartRepo's status
func (r *ChartRepoReconciler) updateChartRepoStatus(ctx context.Context, cr *alaudaiov1alpha1.ChartRepo, phase alaudaiov1alpha1.ChartRepoPhase, reason string) error {
	old := cr.DeepCopy()
	mp := client.MergeFrom(old.DeepCopy())

	old.Status.Phase = phase
	old.Status.Reason = reason

	if phase == alaudaiov1alpha1.ChartRepoSynced {
		now, _ := v1.Now().MarshalQueryParameter()
		if old.Annotations == nil {
			old.Annotations = make(map[string]string)
		}
		old.Annotations["alauda.io/last-sync-at"] = now

	}

	return r.Patch(ctx, old, mp)

}

func (r *ChartRepoReconciler) isReadyForResync(cr *alaudaiov1alpha1.ChartRepo) bool {
	log := r.Log.WithValues("chartrepo", cr.GetName())

	if cr.GetAnnotations() != nil && cr.GetAnnotations()["alauda.io/last-sync-at"] != "" {
		last := cr.GetAnnotations()["alauda.io/last-sync-at"]
		// see: https://stackoverflow.com/questions/25845172/parsing-date-string-in-go
		layout := "2006-01-02T15:04:05Z"
		t, err := time.Parse(layout, last)
		if err != nil {
			log.Error(err, "parse sync time error")
			return true
		}

		now := v1.Now()
		diff := now.Time.Sub(t).Seconds()

		//p, _ := now.MarshalQueryParameter()

		// log.Info("debug timer,", "last", last, "diff", diff)

		if diff >= 90 {
			return true
		}

		return false

	}
	return true

}
