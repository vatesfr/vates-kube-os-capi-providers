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

package pki

import (
	"fmt"
)

const (
	// TLSCrtKey and TLSKeyKey are the keys the control plane provider uses in
	// the <cluster>-ca Secret, the Cluster API convention.
	TLSCrtKey = "tls.crt"
	TLSKeyKey = "tls.key"
)

// ClusterCA is the cluster certificate authority as stored by the CAPI control
// plane provider in the <cluster>-ca Secret. Unlike the operator CA, the
// bootstrap provider never generates it: it reads it so a bootstrapping control
// plane reuses the authority the provider owns instead of minting its own.
//
// The key is only handed to a CONTROL PLANE that is creating the cluster. It
// never reaches a worker, and never a joining control plane.
type ClusterCA struct {
	Cert []byte
	Key  []byte
}

// ClusterCAFromSecretData reads a <cluster>-ca Secret's data. The Secret is the
// Cluster API convention: keys "tls.crt" and "tls.key".
func ClusterCAFromSecretData(data map[string][]byte) (*ClusterCA, error) {
	cert := data[TLSCrtKey]
	if len(cert) == 0 {
		return nil, fmt.Errorf("pki: the cluster CA Secret has no %q", TLSCrtKey)
	}
	return &ClusterCA{Cert: cert, Key: data[TLSKeyKey]}, nil
}
