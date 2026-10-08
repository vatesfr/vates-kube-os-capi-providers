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

package vatesapi

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestClusterCAFromKubeconfig(t *testing.T) {
	const caPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(caPEM))

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{
			name: "extracts the CA certificate from certificate-authority-data",
			raw: fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: vates
  cluster:
    server: https://10.0.0.1:6443
    certificate-authority-data: %s
`, encoded),
			want: caPEM,
		},
		{
			name: "refuses a kubeconfig without a CA",
			raw: `apiVersion: v1
kind: Config
clusters:
- name: vates
  cluster:
    server: https://10.0.0.1:6443
`,
			wantErr: "certificate-authority-data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClusterCAFromKubeconfig([]byte(tt.raw))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ClusterCAFromKubeconfig() error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ClusterCAFromKubeconfig() unexpected error = %v", err)
			}
			if got != tt.want {
				t.Errorf("CA = %q, want %q", got, tt.want)
			}
		})
	}
}
