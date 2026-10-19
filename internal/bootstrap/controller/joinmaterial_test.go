package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vatesfr/vates-kube-os-capi-providers/internal/bootstrap/vatesapi"
)

func TestCachedJoinMaterial(t *testing.T) {
	const (
		cachedToken = "cached.0123456789abcdef"
		freshToken  = "fresh.0123456789abcdef"
	)

	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "vates-mp", Namespace: "default"}}
	key := types.NamespacedName{Namespace: "default", Name: "vates-mp-join-material"}

	// cachedSecret is what a previous fetch left behind. staleSecret dates it
	// past the TTL, which is the case a scale-up hits half an hour after the
	// last one -- and the one that used to hand out credentials the cluster no
	// longer knows.
	cachedSecret := func(fetchedAt time.Time) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Data: map[string][]byte{
				"token":          []byte(cachedToken),
				"certificateKey": []byte("cached-key"),
				"caCertHash":     []byte("cached-hash"),
				"fetchedAt":      []byte(fetchedAt.UTC().Format(time.RFC3339)),
			},
		}
	}
	staleSecret := func() *corev1.Secret { return cachedSecret(time.Now().Add(-time.Hour)) }
	freshSecret := func() *corev1.Secret { return cachedSecret(time.Now()) }

	tests := []struct {
		name       string
		cached     *corev1.Secret
		fetchErr   error
		wantToken  string
		wantStored string
		wantFetch  bool
	}{
		{
			name:       "stores the fetched material when nothing is cached",
			wantToken:  freshToken,
			wantStored: freshToken,
			wantFetch:  true,
		},
		{
			name:       "replaces a stale cache with the fetched material",
			cached:     staleSecret(),
			wantToken:  freshToken,
			wantStored: freshToken,
			wantFetch:  true,
		},
		{
			name:       "reuses a fresh cache without asking a control plane",
			cached:     freshSecret(),
			wantToken:  cachedToken,
			wantStored: cachedToken,
			wantFetch:  false,
		},
		{
			name:       "leaves the cache alone when the fetch fails",
			cached:     staleSecret(),
			fetchErr:   errors.New("no live control plane"),
			wantToken:  "",
			wantStored: cachedToken,
			wantFetch:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("building the scheme: %v", err)
			}
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.cached != nil {
				builder = builder.WithObjects(tt.cached)
			}
			c := builder.Build()

			fetched := false
			fetch := func(context.Context) (vatesapi.JoinMaterial, error) {
				fetched = true
				if tt.fetchErr != nil {
					return vatesapi.JoinMaterial{}, tt.fetchErr
				}
				return vatesapi.JoinMaterial{
					Token:          freshToken,
					CertificateKey: "fresh-key",
					CACertHash:     "fresh-hash",
				}, nil
			}

			r := &VatesConfigReconciler{Client: c, Scheme: scheme}
			got, err := r.cachedJoinMaterial(context.Background(), cluster, fetch)

			switch {
			case tt.fetchErr != nil && err == nil:
				t.Fatalf("cachedJoinMaterial() error = nil, want %v", tt.fetchErr)
			case tt.fetchErr == nil && err != nil:
				t.Fatalf("cachedJoinMaterial() unexpected error: %v", err)
			}
			if got.Token != tt.wantToken {
				t.Errorf("returned token = %q, want %q", got.Token, tt.wantToken)
			}
			if fetched != tt.wantFetch {
				t.Errorf("fetch called = %v, want %v", fetched, tt.wantFetch)
			}

			stored := &corev1.Secret{}
			if err := c.Get(context.Background(), key, stored); err != nil {
				t.Fatalf("reading the join material Secret: %v", err)
			}
			if string(stored.Data["token"]) != tt.wantStored {
				t.Errorf("stored token = %q, want %q", stored.Data["token"], tt.wantStored)
			}
		})
	}
}
