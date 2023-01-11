package manifestwork

import (
	"context"
	"errors"
	"fmt"
	reflect "reflect"

	hubv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api-hub/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	"github.com/kubernetes-sigs/kernel-module-management/internal/constants"
)

var moduleStatusJSONPaths = []workv1.JsonPath{
	{
		Name: "devicePlugin.availableNumber",
		Path: ".status.devicePlugin.availableNumber",
	},
	{
		Name: "devicePlugin.desiredNumber",
		Path: ".status.devicePlugin.desiredNumber",
	},
	{
		Name: "devicePlugin.nodesMatchingSelectorNumber",
		Path: ".status.devicePlugin.nodesMatchingSelectorNumber",
	},
	{
		Name: "moduleLoader.availableNumber",
		Path: ".status.moduleLoader.availableNumber",
	},
	{
		Name: "moduleLoader.desiredNumber",
		Path: ".status.moduleLoader.desiredNumber",
	},
	{
		Name: "moduleLoader.nodesMatchingSelectorNumber",
		Path: ".status.moduleLoader.nodesMatchingSelectorNumber",
	},
}

//go:generate mockgen -source=manifestwork.go -package=manifestwork -destination=mock_manifestwork.go

type ManifestWorkCreator interface {
	GarbageCollect(ctx context.Context, clusters clusterv1.ManagedClusterList, mcm hubv1beta1.ManagedClusterModule) error
	GetOwnedManifestWorks(ctx context.Context, mcm hubv1beta1.ManagedClusterModule) (*workv1.ManifestWorkList, error)
	SetManifestWorkAsDesired(ctx context.Context, mw *workv1.ManifestWork, mcm hubv1beta1.ManagedClusterModule) error
}

type manifestWorkGenerator struct {
	client client.Client
	scheme *runtime.Scheme
}

func NewCreator(client client.Client, scheme *runtime.Scheme) ManifestWorkCreator {
	return &manifestWorkGenerator{
		client: client,
		scheme: scheme,
	}
}

func (mwg *manifestWorkGenerator) GarbageCollect(ctx context.Context, clusters clusterv1.ManagedClusterList, mcm hubv1beta1.ManagedClusterModule) error {
	manifestWorks, err := mwg.getOwnedManifestWorks(ctx, mcm)
	if err != nil {
		return fmt.Errorf("failed to get owned ManifestWorks: %w", err)
	}

	clusterNames := sets.String{}
	for _, cluster := range clusters.Items {
		clusterNames.Insert(cluster.Name)
	}

	for _, mw := range manifestWorks.Items {
		if clusterNames.Has(mw.Namespace) {
			continue
		}

		if err := mwg.client.Delete(ctx, &mw); err != nil {
			return fmt.Errorf("failed to delete ManifestWork %s/%s: %w", mw.Namespace, mw.Name, err)
		}
	}

	return nil
}

func (mwg *manifestWorkGenerator) GetOwnedManifestWorks(ctx context.Context, mcm hubv1beta1.ManagedClusterModule) (*workv1.ManifestWorkList, error) {
	return mwg.getOwnedManifestWorks(ctx, mcm)
}

func (mwg *manifestWorkGenerator) SetManifestWorkAsDesired(ctx context.Context, mw *workv1.ManifestWork, mcm hubv1beta1.ManagedClusterModule) error {
	if mw == nil {
		return errors.New("mw cannot be nil")
	}

	// Ensure no Build and Sign specs reach the Spoke cluster
	mcm.Spec.ModuleSpec.ModuleLoader.Container.Build = nil
	mcm.Spec.ModuleSpec.ModuleLoader.Container.Sign = nil

	kernelMappings := mcm.Spec.ModuleSpec.ModuleLoader.Container.KernelMappings
	for i := 0; i < len(kernelMappings); i++ {
		kernelMappings[i].Build = nil
		kernelMappings[i].Sign = nil
	}

	kind := reflect.TypeOf(kmmv1beta1.Module{}).Name()
	gvk := kmmv1beta1.GroupVersion.WithKind(kind)

	mod := &kmmv1beta1.Module{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gvk.GroupVersion().String(),
			Kind:       kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcm.Name,
			Namespace: mcm.Spec.SpokeNamespace,
		},
		Spec:   mcm.Spec.ModuleSpec,
		Status: kmmv1beta1.ModuleStatus{},
	}

	manifest := workv1.Manifest{
		RawExtension: runtime.RawExtension{Object: mod},
	}

	standardLabels := map[string]string{
		constants.ManagedClusterModuleNameLabel: mcm.Name,
	}

	mw.SetLabels(standardLabels)

	mw.Spec = workv1.ManifestWorkSpec{
		Workload: workv1.ManifestsTemplate{
			Manifests: []workv1.Manifest{manifest},
		},
		ManifestConfigs: []workv1.ManifestConfigOption{
			{
				ResourceIdentifier: workv1.ResourceIdentifier{
					Group:     mod.GroupVersionKind().Group,
					Resource:  "modules",
					Name:      mod.Name,
					Namespace: mod.Namespace,
				},
				FeedbackRules: []workv1.FeedbackRule{
					{
						Type:      workv1.JSONPathsType,
						JsonPaths: moduleStatusJSONPaths,
					},
				},
			},
		},
	}

	return controllerutil.SetControllerReference(&mcm, mw, mwg.scheme)
}

func (mwg *manifestWorkGenerator) getOwnedManifestWorks(ctx context.Context, mcm hubv1beta1.ManagedClusterModule) (*workv1.ManifestWorkList, error) {
	manifestWorkList := &workv1.ManifestWorkList{}

	selector := map[string]string{constants.ManagedClusterModuleNameLabel: mcm.Name}

	opts := []client.ListOption{
		client.MatchingLabelsSelector{
			Selector: labels.Set(selector).AsSelector(),
		},
	}
	err := mwg.client.List(ctx, manifestWorkList, opts...)

	return manifestWorkList, err
}
