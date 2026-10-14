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

// Package controller reconciles VatesBootstrapConfig objects into the bootstrap
// data Secret a Vates Kube OS machine reads at first boot.
package controller

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bootstrapv1 "github.com/vatesfr/vates-kube-os-capi-providers/api/bootstrap/v1alpha1"
	"github.com/vatesfr/vates-kube-os-capi-providers/internal/bootstrap/pki"
	"github.com/vatesfr/vates-kube-os-capi-providers/internal/bootstrap/userdata"
	"github.com/vatesfr/vates-kube-os-capi-providers/internal/bootstrap/vatesapi"

	"github.com/vatesfr/vates-kube-os/vatescfg"
)

// ReadyCondition is the condition the Cluster API bootstrap contract expects a
// bootstrap provider to keep.
const ReadyCondition = clusterv1.ReadyCondition

// bootstrapAnnotation is set by the control plane provider on every control
// plane Machine: "true" for the one that CREATES the cluster, "false" for the
// ones that join. When present, it is authoritative -- the provider decided, so
// the bootstrap provider does not guess.
const bootstrapAnnotation = "vates.io/bootstrap"

// VatesBootstrapConfigReconciler reconciles a VatesBootstrapConfig object.
type VatesBootstrapConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=vatesbootstrapconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=vatesbootstrapconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=vatesbootstrapconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=vatesbootstrapconfigtemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;clusters,verbs=get;list;watch

// Reconcile turns a VatesBootstrapConfig into the vates-node.yaml user-data
// document, published as a Secret the infrastructure provider attaches to the
// machine's config drive.
func (r *VatesBootstrapConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ret ctrl.Result, rerr error) {
	logger := log.FromContext(ctx)

	cfg := &bootstrapv1.VatesBootstrapConfig{}
	if err := r.Get(ctx, req.NamespacedName, cfg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The bootstrap data Secret carries a controller owner reference, so the
	// garbage collector removes it with the config; nothing to finalize.
	if !cfg.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Once the document is published it is fixed: a machine reads its config
	// drive once, and rotating a join token under it would only churn. The
	// bootstrap data Secret is created exactly once.
	if cfg.Status.DataSecretName != nil &&
		cfg.Status.Initialization != nil &&
		ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false) {
		return ctrl.Result{}, nil
	}

	helper, err := patch.NewHelper(cfg, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := helper.Patch(ctx, cfg); err != nil && rerr == nil {
			rerr = err
		}
	}()

	machine, err := util.GetOwnerMachine(ctx, r.Client, cfg.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		logger.Info("VatesBootstrapConfig has no owner Machine yet; waiting")
		return ctrl.Result{}, nil
	}

	clusterName, ok := machine.Labels[clusterv1.ClusterNameLabel]
	if !ok || clusterName == "" {
		logger.Info("Owner Machine has no cluster.x-k8s.io/cluster-name label yet; waiting")
		return ctrl.Result{}, nil
	}
	cluster := &clusterv1.Cluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: machine.Namespace, Name: clusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Owner Cluster is not there yet; waiting", "cluster", clusterName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	out, err := userdata.Assemble(cfg, cluster, machine)
	if err != nil {
		// The document cannot be built from the current spec. Record why on
		// the object rather than requeueing for ever; a spec change drives the
		// next attempt.
		r.markFailed(cfg, "BuildFailed", err.Error())
		return ctrl.Result{}, nil
	}

	// The operator CA is generated once per cluster. A control plane trusts it
	// so the provider can call its management API without ever holding the
	// cluster CA, which the node keeps.
	ca, err := r.ensureOperatorCA(ctx, cluster)
	if err != nil {
		r.markFailed(cfg, "OperatorCAFailed", err.Error())
		return ctrl.Result{}, err
	}
	if out.Role == vatescfg.RoleMaster {
		out.PKI.APICA = vatescfg.CertificateAuthority{
			Cert: string(ca.Cert),
			Key:  string(ca.Key),
		}
	}

	// Does this machine CREATE the cluster, or join one? The control plane
	// provider decides and states it (vates.io/bootstrap); when it does not --
	// a hand-built cluster -- a control plane with no other control plane up
	// creates the cluster, and everything else joins.
	bootstrap := false
	if annotation, ok := machine.Annotations[bootstrapAnnotation]; ok {
		bootstrap = annotation == "true"
	} else if out.Role == vatescfg.RoleMaster {
		_, live, err := r.liveControlPlaneAddress(ctx, cluster, machine.Name, out.APIPort())
		if err != nil {
			return ctrl.Result{}, err
		}
		bootstrap = !live
	}

	// A joining machine draws fresh credentials from a live control plane over
	// the management API: the join material (token, certificate key, CA hash).
	if !bootstrap {
		addr, live, err := r.liveControlPlaneAddress(ctx, cluster, machine.Name, out.APIPort())
		if err != nil {
			return ctrl.Result{}, err
		}
		if !live {
			logger.Info("Waiting for a control plane to join")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		if err := r.fillJoinMaterial(ctx, cluster, out, ca, addr); err != nil {
			r.markFailed(cfg, "JoinMaterialFailed", err.Error())
			return ctrl.Result{}, err
		}
	}

	// When a CAPI control plane provider owns the cluster CA, the bootstrapping
	// control plane reuses it instead of minting its own, and every other
	// machine reads its certificate. Without that provider the node generates
	// the CA and joining machines fetch it from a live control plane.
	requeue, err := r.injectOwnedClusterCA(ctx, cluster, out, bootstrap)
	if err != nil {
		r.markFailed(cfg, "ClusterCAFailed", err.Error())
		return ctrl.Result{}, err
	}
	if requeue {
		logger.Info("Waiting for the control plane provider to publish the cluster CA")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	data, err := out.UserData()
	if err != nil {
		r.markFailed(cfg, "BuildFailed", err.Error())
		return ctrl.Result{}, nil
	}

	secret, err := r.reconcileDataSecret(ctx, cfg, cluster, data)
	if err != nil {
		r.markFailed(cfg, "DataSecretFailed", err.Error())
		return ctrl.Result{}, err
	}

	cfg.Status.Ready = true
	cfg.Status.DataSecretName = ptr.To(secret.Name)
	cfg.Status.Initialization = &bootstrapv1.VatesBootstrapConfigInitialization{DataSecretCreated: ptr.To(true)}
	cfg.Status.FailureReason = nil
	cfg.Status.FailureMessage = nil
	setReady(cfg, metav1.ConditionTrue, "DataSecretAvailable", "Bootstrap data Secret is available")

	logger.Info("Bootstrap data Secret is ready", "secret", secret.Name, "role", userdata.Role(machine))
	return ctrl.Result{}, nil
}

// reconcileDataSecret creates or updates the Secret that carries the document.
// The Secret is named after the config and owned by it, which is the shape the
// Cluster API bootstrap contract and clusterctl expect.
func (r *VatesBootstrapConfigReconciler) reconcileDataSecret(ctx context.Context, cfg *bootstrapv1.VatesBootstrapConfig, cluster *clusterv1.Cluster, data []byte) (*corev1.Secret, error) {
	key := types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Name}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, key, secret); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		secret = desiredSecret(cfg, cluster, data)
		if err := r.Create(ctx, secret); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return nil, err
			}
		}
		return secret, nil
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data["value"] = data
	if err := r.Update(ctx, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// desiredSecret is the Secret a VatesBootstrapConfig publishes: the document
// under the "value" key, labelled with the cluster and controlled by the config.
func desiredSecret(cfg *bootstrapv1.VatesBootstrapConfig, cluster *clusterv1.Cluster, data []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cfg, bootstrapv1.GroupVersion.WithKind("VatesBootstrapConfig")),
			},
		},
		Type: clusterv1.ClusterSecretType,
		Data: map[string][]byte{"value": data},
	}
}

// ensureOperatorCA returns the cluster's operator CA, generating and storing it
// the first time. It lives in a Secret named <cluster>-api-ca, owned by the
// Cluster, so it survives any single control plane.
func (r *VatesBootstrapConfigReconciler) ensureOperatorCA(ctx context.Context, cluster *clusterv1.Cluster) (*pki.OperatorCA, error) {
	name := cluster.Name + "-api-ca"
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: name}

	secret := &corev1.Secret{}
	switch err := r.Get(ctx, key, secret); {
	case err == nil:
		return pki.FromSecretData(secret.Data)
	case !apierrors.IsNotFound(err):
		return nil, err
	}

	ca, err := pki.GenerateOperatorCA(cluster.Name)
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

// clusterCASecretSuffix names the Secret the CAPI control plane provider uses to
// own the cluster certificate authority: <cluster>-ca.
const clusterCASecretSuffix = "-ca"

// injectOwnedClusterCA copies the cluster CA from the <cluster>-ca Secret the
// control plane provider owns into the document. The private KEY is only written
// for a control plane that is CREATING the cluster; a joining control plane and
// a worker get the certificate alone.
//
// When the Secret is absent no CAPI control plane provider owns a CA: the
// bootstrapping node generates one itself and joining machines fetch it from a
// live control plane, which is the fallback path.
func (r *VatesBootstrapConfigReconciler) injectOwnedClusterCA(ctx context.Context, cluster *clusterv1.Cluster, out *vatescfg.Config, bootstrap bool) (bool, error) {
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name + clusterCASecretSuffix}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			// When a Vates control plane provider owns the CA, every machine
			// waits for it: the bootstrapping control plane must not mint its
			// own (it would split the cluster), and a joiner needs the CA
			// certificate to verify the API. Without such a provider, the node
			// generates the CA as before.
			if ownsControlPlane(cluster) {
				return true, nil
			}
			return false, nil
		}
		return false, err
	}

	ca, err := pki.ClusterCAFromSecretData(secret.Data)
	if err != nil {
		return false, err
	}

	out.PKI.ClusterCA = vatescfg.CertificateAuthority{Cert: string(ca.Cert)}
	if bootstrap {
		// The machine that creates the cluster needs the whole authority, or it
		// would half-mint one. Without the key, leave it to generate its own CA.
		if len(ca.Key) == 0 {
			out.PKI.ClusterCA = vatescfg.CertificateAuthority{}
			return false, nil
		}
		out.PKI.ClusterCA.Key = string(ca.Key)
	}
	return false, nil
}

// ownsControlPlane reports whether the Cluster's control plane is a
// VatesControlPlane, i.e. the provider that owns the cluster CA.
func ownsControlPlane(cluster *clusterv1.Cluster) bool {
	ref := cluster.Spec.ControlPlaneRef
	return ref.Kind == "VatesControlPlane" && ref.APIGroup == "controlplane.cluster.x-k8s.io"
}

// liveControlPlaneAddress finds a control plane Machine that already has an
// address, so the provider can ask it for join material. self is skipped: a
// machine cannot join itself.
func (r *VatesBootstrapConfigReconciler) liveControlPlaneAddress(ctx context.Context, cluster *clusterv1.Cluster, self string, port int) (string, bool, error) {
	machines := &clusterv1.MachineList{}
	if err := r.List(ctx, machines, client.InNamespace(cluster.Namespace), client.MatchingLabels{clusterv1.ClusterNameLabel: cluster.Name}); err != nil {
		return "", false, err
	}
	for i := range machines.Items {
		m := &machines.Items[i]
		if m.Name == self || !m.DeletionTimestamp.IsZero() {
			continue
		}
		if _, ok := m.Labels[clusterv1.MachineControlPlaneLabel]; !ok {
			continue
		}
		for _, a := range m.Status.Addresses {
			if a.Type == clusterv1.MachineInternalIP || a.Type == clusterv1.MachineExternalIP {
				return net.JoinHostPort(a.Address, strconv.Itoa(port)), true, nil
			}
		}
	}
	return "", false, nil
}

// fillJoinMaterial asks the control plane at addr for the cluster kubeconfig and
// fresh join credentials, stores the kubeconfig where clusterctl and the addon
// ClusterResourceSets read it, and fills the document with what a joining
// machine needs.
func (r *VatesBootstrapConfigReconciler) fillJoinMaterial(ctx context.Context, cluster *clusterv1.Cluster, out *vatescfg.Config, ca *pki.OperatorCA, addr string) error {
	cl, err := vatesapi.Dial(addr, ca)
	if err != nil {
		return err
	}
	defer cl.Close()

	callCtx, cancel := context.WithTimeout(ctx, joinCallTimeout)
	defer cancel()

	kubeconfig, err := cl.Kubeconfig(callCtx)
	if err != nil {
		return err
	}
	clusterCA, err := vatesapi.ClusterCAFromKubeconfig(kubeconfig)
	if err != nil {
		return err
	}
	if err := r.ensureKubeconfigSecret(ctx, cluster, kubeconfig); err != nil {
		return err
	}

	jm, err := r.cachedJoinMaterial(ctx, cluster, func(context.Context) (vatesapi.JoinMaterial, error) {
		return cl.GetJoinMaterial(callCtx)
	})
	if err != nil {
		return err
	}

	out.PKI.ClusterCA = vatescfg.CertificateAuthority{Cert: clusterCA}
	out.Cluster.Token = jm.Token
	if out.Role == vatescfg.RoleMaster {
		// Only a joining control plane fetches the shared certificates.
		out.Cluster.CertificateKey = jm.CertificateKey
		out.Cluster.CACertHash = jm.CACertHash
	}
	return nil
}

// joinMaterialTTL is how long a cached join material stays usable. kubeadm
// expires the uploaded certificate key after two hours and the bootstrap token
// after twenty-four; half an hour is comfortably inside both, and long enough
// that every machine of one scale-up reuses the same material.
const joinMaterialTTL = 30 * time.Minute

// joinCallTimeout bounds a single GetJoinMaterial/kubeconfig call.
//
// It has to exceed the node's own wait. Answering GetJoinMaterial first waits
// for the controller-manager to publish the bootstrap token's signature -- up to
// thirty attempts two seconds apart, so a minute -- before it uploads the
// certificates. A shorter deadline times the call out while the node is still
// working; the reconcile then retries and the node answers a second time, and on
// a node that mints a fresh certificate key per upload the second answer
// re-encrypts kubeadm-certs and strands the key already written into the first
// document. The fix on the node side makes the upload idempotent; this margin
// keeps us from provoking the retry in the first place.
const joinCallTimeout = 2 * time.Minute

// cachedJoinMaterial returns the cluster's join material, asking a live control
// plane only when none is cached or the cached one is stale.
//
// Fetching it per machine is wrong, and it was the bug that kept joining control
// planes from ever running a control plane. Every GetJoinMaterial call runs
// `kubeadm init phase upload-certs --upload-certs` on the node, which re-encrypts
// the cluster's kubeadm-certs Secret with a FRESH certificate key -- invalidating
// the key already written into every other machine's document. A joining node
// then fails at
//
//	control-plane-prepare/download-certs: error decoding secret data with
//	provided key: cipher: message authentication failed
//
// and, because its kubelet has already registered the Node from the bootstrap
// token, it appears Ready while running no control plane at all.
//
// One fetch serves a whole scale-up, the way a control plane provider hands the
// same certificate key to every machine of a rollout.
//
// fetch asks a live control plane for fresh material; it is a parameter rather
// than a call on a client so the caching rules below can be tested without one.
func (r *VatesBootstrapConfigReconciler) cachedJoinMaterial(ctx context.Context, cluster *clusterv1.Cluster, fetch func(context.Context) (vatesapi.JoinMaterial, error)) (vatesapi.JoinMaterial, error) {
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name + "-join-material"}

	secret := &corev1.Secret{}
	switch err := r.Get(ctx, key, secret); {
	case err == nil:
		if fresh(secret) {
			return joinMaterialFromSecret(secret), nil
		}
	case !apierrors.IsNotFound(err):
		return vatesapi.JoinMaterial{}, err
	}

	// One worker reconciles this controller, so the fetch below never races
	// another: the first machine of a scale-up stores the material, the rest
	// read it back.
	jm, err := fetch(ctx)
	if err != nil {
		return vatesapi.JoinMaterial{}, err
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind("Cluster")),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"token":          []byte(jm.Token),
			"certificateKey": []byte(jm.CertificateKey),
			"caCertHash":     []byte(jm.CACertHash),
			"fetchedAt":      []byte(time.Now().UTC().Format(time.RFC3339)),
		},
	}
	switch err := r.Create(ctx, desired); {
	case err == nil:
		return jm, nil
	case !apierrors.IsAlreadyExists(err):
		return vatesapi.JoinMaterial{}, err
	}

	// The Secret is there but stale. Overwrite it with what was just fetched.
	// Reading it back instead -- which is what this did -- hands the machine the
	// token and certificate key of a previous life of the cluster: the join then
	// fails on a token the cluster does not know, or on a certificate key
	// kubeadm expired two hours after it was uploaded, and the machine never
	// becomes a control plane. A stale cache is the normal case for a scale-up
	// that happens later than half an hour after the last one.
	current := &corev1.Secret{}
	if err := r.Get(ctx, key, current); err != nil {
		return vatesapi.JoinMaterial{}, err
	}
	current.Data = desired.Data
	if err := r.Update(ctx, current); err != nil {
		return vatesapi.JoinMaterial{}, err
	}
	return jm, nil
}

// fresh reports whether a cached join material Secret is still within its TTL.
func fresh(secret *corev1.Secret) bool {
	fetchedAt, err := time.Parse(time.RFC3339, string(secret.Data["fetchedAt"]))
	if err != nil {
		return false
	}
	return time.Since(fetchedAt) < joinMaterialTTL
}

// joinMaterialFromSecret reads a cached join material back.
func joinMaterialFromSecret(secret *corev1.Secret) vatesapi.JoinMaterial {
	return vatesapi.JoinMaterial{
		Token:          string(secret.Data["token"]),
		CertificateKey: string(secret.Data["certificateKey"]),
		CACertHash:     string(secret.Data["caCertHash"]),
	}
}

// ensureKubeconfigSecret publishes the workload cluster kubeconfig under
// <cluster>-kubeconfig, the name Cluster API and its addon ClusterResourceSets
// read. It is fetched once and kept fresh.
func (r *VatesBootstrapConfigReconciler) ensureKubeconfigSecret(ctx context.Context, cluster *clusterv1.Cluster, kubeconfig []byte) error {
	name := cluster.Name + "-kubeconfig"
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: name}

	secret := &corev1.Secret{}
	switch err := r.Get(ctx, key, secret); {
	case err == nil:
		if bytes.Equal(secret.Data["value"], kubeconfig) {
			return nil
		}
		secret.Data["value"] = kubeconfig
		return r.Update(ctx, secret)
	case !apierrors.IsNotFound(err):
		return err
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
		Type: clusterv1.ClusterSecretType,
		Data: map[string][]byte{"value": kubeconfig},
	}
	if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *VatesBootstrapConfigReconciler) markFailed(cfg *bootstrapv1.VatesBootstrapConfig, reason, message string) {
	cfg.Status.FailureReason = ptr.To(reason)
	cfg.Status.FailureMessage = ptr.To(message)
	setReady(cfg, metav1.ConditionFalse, reason, message)
}

func setReady(cfg *bootstrapv1.VatesBootstrapConfig, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type:               ReadyCondition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cfg.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *VatesBootstrapConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootstrapv1.VatesBootstrapConfig{}).
		Owns(&corev1.Secret{}).
		Named("vatesbootstrapconfig").
		Complete(r)
}
