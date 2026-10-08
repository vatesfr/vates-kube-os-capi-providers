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

package userdata

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/vatesfr/vates-kube-os/vatescfg"

	bootstrapv1 "github.com/vatesfr/vates-kube-os-capi-providers/api/bootstrap/v1alpha1"
)

// machineWithLabels builds a CAPI Machine with the given labels, as the
// controller sees it through its owner reference.
func machineWithLabels(labels map[string]string) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-cp-1",
			Namespace: "default",
			Labels:    labels,
		},
	}
}

// clusterWith builds a CAPI Cluster carrying a topology version and an
// endpoint, which is where the provider derives both from.
func clusterWith(version, host string, port int32) *clusterv1.Cluster {
	c := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "default"},
		Spec: clusterv1.ClusterSpec{
			ControlPlaneEndpoint: clusterv1.APIEndpoint{Host: host, Port: port},
		},
	}
	if version != "" {
		c.Spec.Topology = clusterv1.Topology{Version: version}
	}
	return c
}

// validNetwork is the smallest network block a document can carry.
func validNetwork() bootstrapv1.VatesNetwork {
	return bootstrapv1.VatesNetwork{Iface: "eth0", Mode: "dhcp"}
}

// validCNI is the smallest CNI block a document can carry.
func validCNI() bootstrapv1.VatesCNI {
	return bootstrapv1.VatesCNI{Plugin: "flannel", CIDR: "10.244.0.0/16"}
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name        string
		config      *bootstrapv1.VatesBootstrapConfig
		cluster     *clusterv1.Cluster
		machine     *clusterv1.Machine
		wantErr     string
		wantRole    vatescfg.Role
		wantVersion string
		wantEnpd    string
	}{
		{
			name: "control plane derives role, version and endpoint from the CAPI objects",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{Network: validNetwork(), CNI: validCNI()},
			},
			cluster:     clusterWith("v1.31.4", "10.0.0.10", 6443),
			machine:     machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}),
			wantRole:    vatescfg.RoleMaster,
			wantVersion: "v1.31.4",
			wantEnpd:    "10.0.0.10:6443",
		},
		{
			name: "worker is a machine without the control plane label",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{
					Network: validNetwork(),
					CNI:     validCNI(),
					Cluster: bootstrapv1.VatesCluster{Token: "abcdef.0123456789abcdef"},
				},
			},
			cluster:     clusterWith("v1.31.4", "10.0.0.10", 6443),
			machine:     machineWithLabels(nil),
			wantRole:    vatescfg.RoleWorker,
			wantVersion: "v1.31.4",
			wantEnpd:    "10.0.0.10:6443",
		},
		{
			name: "explicit kubernetes version overrides the cluster topology version",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{
					KubernetesVersion: "v1.32.1",
					Network:           validNetwork(),
					CNI:               validCNI(),
				},
			},
			cluster:     clusterWith("v1.31.4", "10.0.0.10", 6443),
			machine:     machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}),
			wantRole:    vatescfg.RoleMaster,
			wantVersion: "v1.32.1",
			wantEnpd:    "10.0.0.10:6443",
		},
		{
			name: "explicit control plane endpoint overrides the cluster endpoint",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{
					Network: validNetwork(),
					CNI:     validCNI(),
					Cluster: bootstrapv1.VatesCluster{ControlPlaneEndpoint: "192.168.1.5:6443"},
				},
			},
			cluster:     clusterWith("v1.31.4", "10.0.0.10", 6443),
			machine:     machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}),
			wantRole:    vatescfg.RoleMaster,
			wantVersion: "v1.31.4",
			wantEnpd:    "192.168.1.5:6443",
		},
		{
			name: "join material is carried through for a joining control plane",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{
					Network: validNetwork(),
					CNI:     validCNI(),
					Cluster: bootstrapv1.VatesCluster{
						Token:          "abcdef.0123456789abcdef",
						CertificateKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
						CACertHash:     strings.Repeat("a", 64),
					},
				},
			},
			cluster:     clusterWith("v1.31.4", "10.0.0.10", 6443),
			machine:     machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}),
			wantRole:    vatescfg.RoleMaster,
			wantVersion: "v1.31.4",
			wantEnpd:    "10.0.0.10:6443",
		},
		{
			name: "no version anywhere is refused",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{Network: validNetwork(), CNI: validCNI()},
			},
			cluster: clusterWith("", "10.0.0.10", 6443),
			machine: machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}),
			wantErr: "kubernetes.version",
		},
		{
			name: "a worker without a token is refused",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{Network: validNetwork(), CNI: validCNI()},
			},
			cluster: clusterWith("v1.31.4", "10.0.0.10", 6443),
			machine: machineWithLabels(nil),
			wantErr: "cluster.token is required for a worker",
		},
		{
			name: "a missing network interface is refused",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{
					Network: bootstrapv1.VatesNetwork{Mode: "dhcp"},
					CNI:     validCNI(),
				},
			},
			cluster: clusterWith("v1.31.4", "10.0.0.10", 6443),
			machine: machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}),
			wantErr: "network.iface is required",
		},
		{
			name:        "a missing cluster is refused",
			config:      &bootstrapv1.VatesBootstrapConfig{Spec: bootstrapv1.VatesBootstrapConfigSpec{Network: validNetwork(), CNI: validCNI()}},
			cluster:     nil,
			machine:     machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}),
			wantErr:     "owner Cluster is required",
			wantVersion: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Build(tt.config, tt.cluster, tt.machine)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Build() error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Build() error = %q, want it to contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build() unexpected error = %v", err)
			}

			if got.Role != tt.wantRole {
				t.Errorf("Role = %q, want %q", got.Role, tt.wantRole)
			}
			if got.Kubernetes.Version != tt.wantVersion {
				t.Errorf("Kubernetes.Version = %q, want %q", got.Kubernetes.Version, tt.wantVersion)
			}
			if got.Cluster.ControlPlaneEndpoint != tt.wantEnpd {
				t.Errorf("ControlPlaneEndpoint = %q, want %q", got.Cluster.ControlPlaneEndpoint, tt.wantEnpd)
			}
			if tt.machine != nil && got.Node.Name != tt.machine.Name {
				t.Errorf("Node.Name = %q, want the Machine name %q", got.Node.Name, tt.machine.Name)
			}
		})
	}
}

func TestUserDataRoundTripsThroughTheOSSchema(t *testing.T) {
	tests := []struct {
		name   string
		config *bootstrapv1.VatesBootstrapConfig
		check  func(t *testing.T, got *vatescfg.Config)
	}{
		{
			name: "optional blocks are carried into the document",
			config: &bootstrapv1.VatesBootstrapConfig{
				Spec: bootstrapv1.VatesBootstrapConfigSpec{
					Network: validNetwork(),
					CNI:     validCNI(),
					Cluster: bootstrapv1.VatesCluster{
						VIP: &bootstrapv1.VatesVIP{Address: "10.0.0.10", Interface: "eth0"},
					},
					Registry: &bootstrapv1.VatesRegistry{
						Kubernetes: "harbor.example.com/k8s",
						Mirrors:    []bootstrapv1.VatesRegistryMirror{{Host: "ghcr.io", Replace: "harbor.example.com/mirror/ghcr.io"}},
					},
					Binaries:  &bootstrapv1.VatesBinaries{Base: "https://mirror.example.com/k8s/release"},
					Dashboard: &bootstrapv1.VatesDashboard{Mode: "tui"},
					API:       &bootstrapv1.VatesAPI{Port: 50001},
					Cloud:     &bootstrapv1.VatesCloud{Provider: "external"},
					Time:      &bootstrapv1.VatesTime{Servers: []string{"ntp.example.com"}},
				},
			},
			check: func(t *testing.T, got *vatescfg.Config) {
				t.Helper()
				if got.Cluster.VIP.Address != "10.0.0.10" {
					t.Errorf("VIP.Address = %q, want 10.0.0.10", got.Cluster.VIP.Address)
				}
				if got.Registry.Kubernetes != "harbor.example.com/k8s" {
					t.Errorf("Registry.Kubernetes = %q, want harbor.example.com/k8s", got.Registry.Kubernetes)
				}
				if len(got.Registry.Mirrors) != 1 || got.Registry.Mirrors[0].Host != "ghcr.io" {
					t.Errorf("Registry.Mirrors = %+v, want one ghcr.io rule", got.Registry.Mirrors)
				}
				if got.Binaries.Base != "https://mirror.example.com/k8s/release" {
					t.Errorf("Binaries.Base = %q, want the mirror", got.Binaries.Base)
				}
				if got.Dashboard.Mode != vatescfg.DashboardTUI {
					t.Errorf("Dashboard.Mode = %q, want tui", got.Dashboard.Mode)
				}
				if got.API.Port != 50001 {
					t.Errorf("API.Port = %d, want 50001", got.API.Port)
				}
				if got.Cloud.Provider != vatescfg.CloudProviderExternal {
					t.Errorf("Cloud.Provider = %q, want external", got.Cloud.Provider)
				}
				if len(got.Time.Servers) != 1 || got.Time.Servers[0] != "ntp.example.com" {
					t.Errorf("Time.Servers = %v, want ntp.example.com", got.Time.Servers)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cluster := clusterWith("v1.31.4", "10.0.0.10", 6443)
			machine := machineWithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""})

			raw, err := UserData(tt.config, cluster, machine)
			if err != nil {
				t.Fatalf("UserData() unexpected error = %v", err)
			}

			// The node reads the document through the OS schema; round-tripping
			// here proves the provider emits something the node accepts.
			doc, ok := vatescfg.FromUserData(raw)
			if !ok {
				t.Fatal("FromUserData() reported no content")
			}
			got, err := vatescfg.Load(doc)
			if err != nil {
				t.Fatalf("the OS schema rejected the provider's document: %v", err)
			}
			if got.Node.Name != "cluster-cp-1" {
				t.Errorf("Node.Name = %q, want the Machine name cluster-cp-1", got.Node.Name)
			}

			tt.check(t, got)
		})
	}
}
