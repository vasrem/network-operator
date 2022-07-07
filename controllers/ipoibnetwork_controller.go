/*
2022 NVIDIA CORPORATION & AFFILIATES

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

package controllers //nolint:dupl

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mellanoxcomv1alpha1 "github.com/Mellanox/network-operator/api/v1alpha1"
	"github.com/Mellanox/network-operator/pkg/config"
	"github.com/Mellanox/network-operator/pkg/consts"
	"github.com/Mellanox/network-operator/pkg/state"
	"github.com/Mellanox/network-operator/pkg/utils"
)

// IPoIBNetworkReconciler reconciles a IPoIBNetwork object
type IPoIBNetworkReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	stateManager state.Manager
}

// +kubebuilder:rbac:groups=mellanox.com,resources=ipoibnetworks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mellanox.com,resources=ipoibnetworks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mellanox.com,resources=*,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=*,verbs=*

//nolint:dupl
// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *IPoIBNetworkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := r.Log.WithValues("ipoibnetwork", req.NamespacedName)
	reqLogger.Info("Reconciling IPoIBNetwork")

	// Fetch the IPoIBNetwork instance
	instance := &mellanoxcomv1alpha1.IPoIBNetwork{}
	err := r.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	managerStatus, managerErr := r.stateManager.SyncState(instance, nil)
	err = r.updateCrStatus(instance, managerStatus, managerErr)
	if err != nil {
		return reconcile.Result{}, err
	}

	if managerStatus.Status != state.SyncStateReady {
		return reconcile.Result{
			RequeueAfter: time.Duration(config.FromEnv().Controller.RequeueTimeSeconds) * time.Second,
		}, nil
	}

	return ctrl.Result{}, nil
}

func (r *IPoIBNetworkReconciler) updateCrStatus(cr *mellanoxcomv1alpha1.IPoIBNetwork, status state.Results,
	syncError error) error {
	cr.Status.State = mellanoxcomv1alpha1.State(status.StatesStatus[0].Status)
	if syncError != nil {
		cr.Status.Reason = syncError.Error()
	}

	var err error

	if cr.Status.State == state.SyncStateReady {
		netAttachDef := &netattdefv1.NetworkAttachmentDefinition{}
		getErr := r.Get(context.TODO(),
			types.NamespacedName{
				Name:      cr.Name,
				Namespace: cr.Spec.NetworkNamespace,
			}, netAttachDef)

		if getErr != nil {
			r.Log.V(consts.LogLevelError).Info("Can not retrieve NetworkAttachmentDefinition object", "error:", getErr)
			err = getErr
		} else {
			cr.Status.IPoIBNetworkAttachmentDef = utils.GetNetworkAttachmentDefLink(netAttachDef)
		}
	}

	// send status update request to k8s API
	r.Log.V(consts.LogLevelInfo).Info(
		"Updating status", "Custom resource name", cr.Name, "namespace", cr.Namespace, "Result:", cr.Status)
	updateErr := r.Status().Update(context.TODO(), cr)
	if updateErr != nil {
		r.Log.V(consts.LogLevelError).Info("Failed to update CR status", "error:", updateErr)
		err = updateErr
	}

	return err
}

// SetupWithManager sets up the controller with the Manager.
//nolint:dupl
func (r *IPoIBNetworkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create state manager
	stateManager, err := state.NewManager(mellanoxcomv1alpha1.IPoIBNetworkCRDName, mgr.GetClient(), mgr.GetScheme())
	if err != nil {
		// Error creating stateManager
		r.Log.V(consts.LogLevelError).Info("Error creating state manager.", "error:", err)
		panic("Failed to create State manager")
	}
	r.stateManager = stateManager

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&mellanoxcomv1alpha1.IPoIBNetwork{}).
		// Watch for changes to primary resource IPoIBNetwork
		Watches(&source.Kind{Type: &mellanoxcomv1alpha1.IPoIBNetwork{}}, &handler.EnqueueRequestForObject{})

	// Watch for changes to secondary resource DaemonSet and requeue the owner IPoIBNetwork
	ws := stateManager.GetWatchSources()
	r.Log.V(consts.LogLevelInfo).Info("Watch Sources", "Kind:", ws)
	for i := range ws {
		builder = builder.Watches(ws[i], &handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &mellanoxcomv1alpha1.IPoIBNetwork{},
		})
	}

	return builder.Complete(r)
}