/*
Copyright 2026.

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

// Package controller reconciles a VatesControlPlane into the cluster's control
// plane: it owns the cluster certificate authority, stamps out the control
// plane Machines from the declared templates, and reports the Cluster API
// control plane contract so the Cluster can move forward.
package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	controlplanev1alpha1 "github.com/vatesfr/vates-kube-os-capi-providers/api/controlplane/v1alpha1"
	"github.com/vatesfr/vates-kube-os-capi-providers/internal/controlplane/etcd"
	"github.com/vatesfr/vates-kube-os-capi-providers/internal/controlplane/pki"
)

// VatesControlPlaneFinalizer is set on a VatesControlPlane so it can hold its
// deletion until its control plane Machines are gone.
const VatesControlPlaneFinalizer = "vatescontrolplane.controlplane.cluster.x-k8s.io"

// BootstrapAnnotation marks the control plane Machine that CREATES the cluster.
// The bootstrap provider honours it instead of guessing from whether a control
// plane is already up, so all replicas can be created at once (as KCP does).
// Every machine carries it: "true" for the first, "false" for the rest.
const BootstrapAnnotation = "vates.io/bootstrap"

// VatesControlPlaneReconciler reconciles a VatesControlPlane object.
type VatesControlPlaneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=vatescontrolplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=vatescontrolplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=vatescontrolplanes/finalizers,verbs=update
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=vatescontrolplanetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=vates.infrastructure.cluster.x-k8s.io,resources=xomachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vates.infrastructure.cluster.x-k8s.io,resources=xomachinetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=vatesbootstrapconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=vatesbootstrapconfigtemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

// Reconcile turns a VatesControlPlane into a running control plane: it ensures
// the cluster certificate authority exists, reconciles the desired number of
// control plane Machines, and reports the contract status.
func (r *VatesControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ret ctrl.Result, rerr error) {
	logger := log.FromContext(ctx)

	vcp := &controlplanev1alpha1.VatesControlPlane{}
	if err := r.Get(ctx, req.NamespacedName, vcp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !vcp.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, vcp)
	}

	// The Cluster is the owner (or, for a flat cluster, is named by the
	// cluster-name label). Without it nothing can be derived.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, vcp.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		cluster, err = util.GetClusterFromMetadata(ctx, r.Client, vcp.ObjectMeta)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	if cluster == nil {
		logger.Info("VatesControlPlane has no owner Cluster yet; waiting")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	helper, err := patch.NewHelper(vcp, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := helper.Patch(ctx, vcp); err != nil && rerr == nil {
			rerr = err
		}
	}()

	// Add the finalizer (persisted by the patch helper at the end) so deletion
	// can wait for the Machines to go.
	controllerutil.AddFinalizer(vcp, VatesControlPlaneFinalizer)

	// 1. The certificate authority is generated once per cluster and kept in
	//    the management cluster. Owning it is what lets the provider inject it
	//    into the bootstrapping node instead of letting that node mint one.
	if _, err := r.ensureClusterCA(ctx, cluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring the cluster CA: %w", err)
	}

	// 1b. The cluster CA is ours, so we mint the workload cluster's admin
	//     kubeconfig from it and publish it where Cluster API reads it. This is
	//     what lets the Machine controller see the workload Node and set
	//     status.nodeRef -- and therefore what lets the control plane report
	//     itself initialized for the FIRST control plane, with no live node to
	//     ask.
	kubeconfigReady, err := r.reconcileKubeconfig(ctx, vcp, cluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling the cluster kubeconfig: %w", err)
	}

	// 2. Reconcile the control plane Machines to the desired count.
	machines, err := r.reconcileMachines(ctx, vcp, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 3. Report the contract status.
	//
	// "Initialized" means the control plane can accept requests. We own the
	// kubeconfig, so we ask the control plane directly instead of waiting for
	// the Machine controller to observe the workload Node -- which also depends
	// on the cloud controller manager having set the Node's providerID.
	r.updateStatus(vcp, cluster, machines, r.controlPlaneReachable(ctx, cluster))

	// The kubeconfig depends on the CA being visible through the cache; if it
	// is not yet, come back shortly rather than waiting for an unrelated event.
	if !kubeconfigReady {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// controlPlaneMachineLabels are the labels every control plane Machine (and the
// objects it references) carries: the cluster it belongs to, the fact that it is
// a control plane, and which control plane owns it.
func controlPlaneMachineLabels(cluster *clusterv1.Cluster, vcp *controlplanev1alpha1.VatesControlPlane) map[string]string {
	out := map[string]string{
		clusterv1.ClusterNameLabel:             cluster.Name,
		clusterv1.MachineControlPlaneLabel:     "",
		clusterv1.MachineControlPlaneNameLabel: vcp.Name,
	}
	for k, v := range vcp.Spec.MachineTemplate.Metadata.Labels {
		out[k] = v
	}
	return out
}

// selectorLabels are the labels that identify this control plane's Machines.
func selectorLabels(cluster *clusterv1.Cluster, vcp *controlplanev1alpha1.VatesControlPlane) map[string]string {
	return map[string]string{
		clusterv1.ClusterNameLabel:             cluster.Name,
		clusterv1.MachineControlPlaneNameLabel: vcp.Name,
	}
}

// selectorFor is the label selector matching this control plane's Machines. It
// is published as status.selector, which the scale subresource and kubectl
// describe read.
func selectorFor(cluster *clusterv1.Cluster, vcp *controlplanev1alpha1.VatesControlPlane) labels.Selector {
	return labels.SelectorFromSet(labels.Set(selectorLabels(cluster, vcp)))
}

// reconcileMachines brings the number of control plane Machines to the desired
// replicas and returns the current list. Scaling is deliberately simple for
// now: it adds missing indices and deletes the highest ones; rolling updates
// come later.
func (r *VatesControlPlaneReconciler) reconcileMachines(ctx context.Context, vcp *controlplanev1alpha1.VatesControlPlane, cluster *clusterv1.Cluster) ([]clusterv1.Machine, error) {
	logger := log.FromContext(ctx)

	machines := &clusterv1.MachineList{}
	if err := r.List(ctx, machines,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(selectorLabels(cluster, vcp)),
	); err != nil {
		return nil, err
	}

	desired := int(ptr.Deref(vcp.Spec.Replicas, 1))

	// Index the machines we already have by their numeric suffix. A Machine that
	// is going away is indexed separately rather than dropped: its index is
	// still taken by the name it holds until it is gone.
	byIndex := map[int]clusterv1.Machine{}
	terminating := map[int]clusterv1.Machine{}
	for i := range machines.Items {
		m := machines.Items[i]
		idx, ok := machineIndex(vcp.Name, m.Name)
		if !ok {
			continue
		}
		if !m.DeletionTimestamp.IsZero() {
			terminating[idx] = m
			continue
		}
		byIndex[idx] = m
	}

	// Every control plane machine on its way out takes its etcd member with it,
	// and does so before the scale-up below fills its index. That order is the
	// whole point: adding the replacement's member first raises the quorum --
	// three members with one down go from needing two votes to needing three --
	// which buys no fault tolerance and leaves the cluster one failure away from
	// losing its data for good.
	//
	// The removal runs on a control plane that is staying, never on the member
	// being removed: that member is the one most likely to be down, which is
	// usually why it is being replaced at all.
	departing := departingNames(byIndex, terminating, desired)
	if host, ok := etcdHost(byIndex, departing, desired); ok && len(departing) > 0 {
		if err := r.removeEtcdMembers(ctx, cluster, host, departing); err != nil {
			return nil, err
		}
	}

	// Scale down: delete the highest indices beyond the desired count.
	for idx := desired; ; idx++ {
		m, ok := byIndex[idx]
		if !ok {
			break
		}
		logger.Info("Deleting control plane Machine", "machine", m.Name)
		if err := r.deleteMachineAndDependents(ctx, &m); err != nil {
			return nil, err
		}
		delete(byIndex, idx)
	}

	// Scale up: create the missing indices.
	for _, idx := range pendingIndices(desired, byIndex, terminating) {
		if err := r.createControlPlaneMachine(ctx, vcp, cluster, idx); err != nil {
			return nil, err
		}
	}

	// Return the freshly listed state.
	fresh := &clusterv1.MachineList{}
	if err := r.List(ctx, fresh,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(selectorLabels(cluster, vcp)),
	); err != nil {
		return nil, err
	}
	return fresh.Items, nil
}

// pendingIndices returns the indices the control plane still has to create:
// every index below desired that no live Machine occupies and no terminating
// Machine is still holding.
//
// A terminating Machine holds its index on purpose. It keeps its name until it
// is gone, and it is gone only once the infrastructure object it references has
// been deleted -- which the Machine controller does on its own. Recreating the
// index now would recreate that object under the same name, and the two
// controllers would fight over it: the Machine controller deleting it, this one
// recreating it, with a VM created on every round and the Machine never
// finishing its deletion. Owns(&Machine{}) requeues this reconcile once the
// name is free.
func pendingIndices(desired int, live, terminating map[int]clusterv1.Machine) []int {
	var out []int
	for idx := 0; idx < desired; idx++ {
		if _, ok := live[idx]; ok {
			continue
		}
		if _, ok := terminating[idx]; ok {
			continue
		}
		out = append(out, idx)
	}
	return out
}

// departingNames lists the control plane machines that are on their way out:
// the ones already terminating, plus the ones above the desired count that the
// scale-down is about to delete. Their etcd members go with them.
func departingNames(live, terminating map[int]clusterv1.Machine, desired int) []string {
	names := make([]string, 0, len(terminating))
	for idx := range terminating {
		names = append(names, terminating[idx].Name)
	}
	for idx := desired; ; idx++ {
		m, ok := live[idx]
		if !ok {
			break
		}
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return names
}

// etcdHost picks the control plane whose etcd pod the removal runs in: one that
// is staying. Any member can remove any other, so there is never a reason to
// run it on the one going away -- and a good reason not to, since that member is
// the one most likely to be down.
func etcdHost(live map[int]clusterv1.Machine, departing []string, desired int) (string, bool) {
	gone := make(map[string]bool, len(departing))
	for _, name := range departing {
		gone[name] = true
	}
	for idx := 0; idx < desired; idx++ {
		m, ok := live[idx]
		if !ok || gone[m.Name] {
			continue
		}
		return m.Name, true
	}
	return "", false
}

// removeEtcdMembers takes the etcd member of each departing machine out of the
// cluster, through the etcd pod running on host.
//
// A member that is already gone is not an error: this is retried on every
// reconcile, so the second attempt has to be a no-op. A failure that would leave
// a member behind IS returned, because that is the state this step exists to
// prevent -- the next machine to replace the one that left would be added on top
// of a member that is still counted.
func (r *VatesControlPlaneReconciler) removeEtcdMembers(ctx context.Context, cluster *clusterv1.Cluster, host string, names []string) error {
	logger := log.FromContext(ctx)

	client, err := r.etcdClient(ctx, cluster, host)
	if err != nil {
		return err
	}
	for _, name := range names {
		removed, err := client.RemoveMemberNamed(ctx, name)
		if err != nil {
			return fmt.Errorf("removing the etcd member of %s: %w", name, err)
		}
		if removed {
			logger.Info("Removed the etcd member of a departing control plane machine", "machine", name)
		}
	}
	return nil
}

// etcdClient reaches etcd through the static pod that runs it on host. The
// provider owns the cluster's admin kubeconfig, and the pod carries etcdctl and
// the member's certificates, so nothing else is needed to talk to etcd.
func (r *VatesControlPlaneReconciler) etcdClient(ctx context.Context, cluster *clusterv1.Cluster, host string) (*etcd.Client, error) {
	raw, err := kubeconfig.FromSecret(ctx, r.Client, util.ObjectKey(cluster))
	if err != nil {
		return nil, fmt.Errorf("reading the cluster kubeconfig: %w", err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("building the workload cluster client: %w", err)
	}
	// Long enough for an exec into a busy pod, short enough that a reconcile
	// does not park on a control plane that is going away.
	cfg.Timeout = 30 * time.Second
	return etcd.New(cfg, host)
}

// machineIndex extracts the numeric index from a Machine name shaped
// <controlPlaneName>-<index>.
func machineIndex(controlPlaneName, machineName string) (int, bool) {
	prefix := controlPlaneName + "-"
	if !strings.HasPrefix(machineName, prefix) {
		return 0, false
	}
	idx, err := strconv.Atoi(strings.TrimPrefix(machineName, prefix))
	if err != nil {
		return 0, false
	}
	return idx, true
}

// createControlPlaneMachine creates one control plane Machine and the two
// objects it references: a VatesBootstrapConfig stamped from the bootstrap
// template, and an XOMachine stamped from the infrastructure template.
func (r *VatesControlPlaneReconciler) createControlPlaneMachine(ctx context.Context, vcp *controlplanev1alpha1.VatesControlPlane, cluster *clusterv1.Cluster, index int) error {
	name := fmt.Sprintf("%s-%d", vcp.Name, index)
	selectorLabels := controlPlaneMachineLabels(cluster, vcp)
	owner := metav1.NewControllerRef(vcp, controlplanev1alpha1.GroupVersion.WithKind("VatesControlPlane"))

	annotations := map[string]string{}
	for k, v := range vcp.Spec.MachineTemplate.Metadata.Annotations {
		annotations[k] = v
	}
	// Designate the machine that creates the cluster: index 0. The bootstrap
	// provider honours it rather than guessing from a live control plane.
	if index == 0 {
		annotations[BootstrapAnnotation] = "true"
	} else {
		annotations[BootstrapAnnotation] = "false"
	}

	// The bootstrap document: a VatesBootstrapConfig cloned from the declared
	// template. The template carries the cluster-independent document; the
	// bootstrap provider adds the role, the CA and the join material.
	bootstrapRef := vcp.Spec.MachineTemplate.BootstrapConfigTemplateRef.DeepCopy()
	bootstrapRef.Namespace = cluster.Namespace
	_, bootstrapObjRef, err := external.CreateFromTemplate(ctx, &external.CreateFromTemplateInput{
		Client:      r.Client,
		TemplateRef: bootstrapRef,
		Namespace:   cluster.Namespace,
		Name:        name,
		ClusterName: cluster.Name,
		OwnerRef:    owner,
		Labels:      selectorLabels,
		Annotations: annotations,
	})
	if err != nil {
		return fmt.Errorf("creating the bootstrap config for machine %q: %w", name, err)
	}

	// The VM: an XOMachine cloned from the declared infrastructure template. The
	// infrastructure provider attaches the bootstrap Secret as the config drive.
	infraRef := vcp.Spec.MachineTemplate.InfrastructureRef.DeepCopy()
	infraRef.Namespace = cluster.Namespace
	_, infraObjRef, err := external.CreateFromTemplate(ctx, &external.CreateFromTemplateInput{
		Client:      r.Client,
		TemplateRef: infraRef,
		Namespace:   cluster.Namespace,
		Name:        name,
		ClusterName: cluster.Name,
		OwnerRef:    owner,
		Labels:      selectorLabels,
		Annotations: annotations,
	})
	if err != nil {
		return fmt.Errorf("creating the infrastructure machine for machine %q: %w", name, err)
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       cluster.Namespace,
			Labels:          selectorLabels,
			Annotations:     annotations,
			OwnerReferences: []metav1.OwnerReference{*owner},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName:       cluster.Name,
			Version:           vcp.Spec.Version,
			Bootstrap:         clusterv1.Bootstrap{ConfigRef: bootstrapObjRef},
			InfrastructureRef: infraObjRef,
		},
	}
	if err := r.Create(ctx, machine); err != nil {
		return fmt.Errorf("creating control plane machine %q: %w", name, err)
	}
	return nil
}

// deleteMachineAndDependents deletes a Machine and the bootstrap and
// infrastructure objects it references, so a scale-down leaves nothing behind.
func (r *VatesControlPlaneReconciler) deleteMachineAndDependents(ctx context.Context, machine *clusterv1.Machine) error {
	for _, ref := range []clusterv1.ContractVersionedObjectReference{
		machine.Spec.Bootstrap.ConfigRef,
		machine.Spec.InfrastructureRef,
	} {
		if ref.Name == "" {
			continue
		}
		obj, err := external.GetObjectFromContractVersionedRef(ctx, r.Client, ref, machine.Namespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if err := r.Delete(ctx, machine); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// ensureClusterCA returns the cluster CA, generating and storing it in the
// <cluster>-ca Secret the first time. The Secret is owned by the Cluster, not by
// the VatesControlPlane, so recreating the control plane object keeps the same
// authority.
func (r *VatesControlPlaneReconciler) ensureClusterCA(ctx context.Context, cluster *clusterv1.Cluster) (*pki.ClusterCA, error) {
	name := cluster.Name + "-" + pki.SecretSuffix
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: name}

	secret := &corev1.Secret{}
	switch err := r.Get(ctx, key, secret); {
	case err == nil:
		return pki.FromSecretData(secret.Data)
	case !apierrors.IsNotFound(err):
		return nil, err
	}

	ca, err := pki.GenerateClusterCA()
	if err != nil {
		return nil, err
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind("Cluster")),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: ca.SecretData(),
	}
	if err := r.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		// Another reconcile created it first; read it back.
		if err := r.Get(ctx, key, secret); err != nil {
			return nil, err
		}
		return pki.FromSecretData(secret.Data)
	}
	return ca, nil
}

// reconcileKubeconfig mints the workload cluster's admin kubeconfig from the
// cluster CA this provider owns and stores it under <cluster>-kubeconfig, where
// Cluster API reads it to inspect the workload cluster. Without it Cluster API
// cannot see the first control plane's Node, so Machine.status.nodeRef would
// stay empty and the control plane would never report itself initialized.
//
// It returns false when the secret is not there yet but should be retried.
func (r *VatesControlPlaneReconciler) reconcileKubeconfig(ctx context.Context, vcp *controlplanev1alpha1.VatesControlPlane, cluster *clusterv1.Cluster) (bool, error) {
	logger := log.FromContext(ctx)

	if cluster.Spec.ControlPlaneEndpoint.IsZero() {
		return true, nil
	}

	key := util.ObjectKey(cluster)
	switch _, err := secret.Get(ctx, r.Client, key, secret.Kubeconfig); {
	case err == nil:
		// Already published. Cluster API owns rotation from here.
		return true, nil
	case !apierrors.IsNotFound(err):
		return false, err
	}

	owner := *metav1.NewControllerRef(vcp, controlplanev1alpha1.GroupVersion.WithKind("VatesControlPlane"))
	if err := kubeconfig.CreateSecretWithOwner(ctx, r.Client, key, cluster.Spec.ControlPlaneEndpoint.String(), owner); err != nil {
		if errors.Is(err, kubeconfig.ErrDependentCertificateNotFound) {
			// The CA Secret is not visible through the cache yet; retry soon.
			logger.Info("Cluster CA not visible yet; waiting to publish the kubeconfig")
			return false, nil
		}
		return false, err
	}
	logger.Info("Published the cluster kubeconfig", "secret", key.Name+"-kubeconfig")
	return true, nil
}

// controlPlaneReachable reports whether the workload cluster's API answers. It
// uses the kubeconfig this provider minted, so it needs no help from the Machine
// controller or the cloud controller manager: the moment the API server is up
// behind the endpoint, the control plane is initialized.
func (r *VatesControlPlaneReconciler) controlPlaneReachable(ctx context.Context, cluster *clusterv1.Cluster) bool {
	raw, err := kubeconfig.FromSecret(ctx, r.Client, util.ObjectKey(cluster))
	if err != nil {
		return false
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return false
	}
	cfg.Timeout = 5 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = cs.Discovery().RESTClient().Get().AbsPath("/healthz").Do(probeCtx).Raw()
	return err == nil
}

// updateStatus fills the Cluster API control plane contract fields and the two
// conditions the Cluster controller watches.
func (r *VatesControlPlaneReconciler) updateStatus(vcp *controlplanev1alpha1.VatesControlPlane, cluster *clusterv1.Cluster, machines []clusterv1.Machine, reachable bool) {
	// Stable order, so describe output and scale-down order are predictable.
	sort.Slice(machines, func(i, j int) bool { return machines[i].Name < machines[j].Name })

	var ready, available, upToDate int32
	// The control plane is initialized as soon as its API answers; a Machine's
	// nodeRef is a second, slower confirmation (it needs the CCM to have set the
	// Node's providerID) that we take as true too.
	initialized := reachable
	for i := range machines {
		m := &machines[i]
		if apimeta.IsStatusConditionTrue(m.Status.Conditions, clusterv1.MachineReadyCondition) {
			ready++
		}
		if apimeta.IsStatusConditionTrue(m.Status.Conditions, clusterv1.MachineAvailableCondition) {
			available++
		}
		if m.Spec.Version == vcp.Spec.Version {
			upToDate++
		}
		if m.Status.NodeRef.IsDefined() {
			initialized = true
		}
	}

	replicas := int32(len(machines))
	desired := ptr.Deref(vcp.Spec.Replicas, 1)

	vcp.Status.Replicas = ptr.To(replicas)
	vcp.Status.ReadyReplicas = ptr.To(ready)
	vcp.Status.AvailableReplicas = ptr.To(available)
	vcp.Status.UpToDateReplicas = ptr.To(upToDate)
	vcp.Status.Version = vcp.Spec.Version
	vcp.Status.ObservedGeneration = vcp.Generation
	vcp.Status.Initialization.ControlPlaneInitialized = ptr.To(initialized)
	vcp.Status.Selector = selectorFor(cluster, vcp).String()

	availableCondition := metav1.ConditionFalse
	availableReason := "NotInitialized"
	availableMessage := "Waiting for the first control plane machine to register a node"
	switch {
	case replicas == desired && ready == desired && initialized:
		availableCondition = metav1.ConditionTrue
		availableReason = "Available"
		availableMessage = "The control plane is available"
	case initialized:
		availableReason = "ScalingUp"
		availableMessage = fmt.Sprintf("%d of %d control plane machines are ready", ready, desired)
	}
	apimeta.SetStatusCondition(&vcp.Status.Conditions, metav1.Condition{
		Type:               clusterv1.AvailableCondition,
		Status:             availableCondition,
		Reason:             availableReason,
		Message:            availableMessage,
		ObservedGeneration: vcp.Generation,
	})

	machinesReady := metav1.ConditionTrue
	machinesReadyReason := "AllMachinesReady"
	machinesReadyMessage := "All control plane machines are ready"
	if replicas != desired || ready != desired {
		machinesReady = metav1.ConditionFalse
		machinesReadyReason = "WaitingForMachines"
		machinesReadyMessage = fmt.Sprintf("%d of %d control plane machines are ready", ready, desired)
	}
	apimeta.SetStatusCondition(&vcp.Status.Conditions, metav1.Condition{
		Type:               clusterv1.MachinesReadyCondition,
		Status:             machinesReady,
		Reason:             machinesReadyReason,
		Message:            machinesReadyMessage,
		ObservedGeneration: vcp.Generation,
	})
}

// reconcileDelete holds deletion until the control plane Machines are gone, then
// releases the finalizer.
func (r *VatesControlPlaneReconciler) reconcileDelete(ctx context.Context, vcp *controlplanev1alpha1.VatesControlPlane) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(vcp, VatesControlPlaneFinalizer) {
		return ctrl.Result{}, nil
	}

	machines := &clusterv1.MachineList{}
	if err := r.List(ctx, machines, client.InNamespace(vcp.Namespace), client.MatchingLabels(map[string]string{
		clusterv1.MachineControlPlaneNameLabel: vcp.Name,
	})); err != nil {
		return ctrl.Result{}, err
	}
	for i := range machines.Items {
		if !machines.Items[i].DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.deleteMachineAndDependents(ctx, &machines.Items[i]); err != nil {
			return ctrl.Result{}, err
		}
	}
	if len(machines.Items) > 0 {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	controllerutil.RemoveFinalizer(vcp, VatesControlPlaneFinalizer)
	if err := r.Update(ctx, vcp); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VatesControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1alpha1.VatesControlPlane{}).
		Owns(&clusterv1.Machine{}).
		Named("vatescontrolplane").
		Complete(r)
}
