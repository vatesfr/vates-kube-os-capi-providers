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

// Package pki mints and stores the certificate authority of a Vates Kube OS
// cluster.
//
// Under Cluster API the control plane provider owns the cluster CA: it
// generates it once, keeps it in the management cluster, and injects it into
// the bootstrap document of every control plane machine. A bootstrapping node
// therefore REUSES this authority instead of minting its own (Vates Kube OS
// supports the injected CA in vates-init), which is what lets the provider
// survive the loss of any single control plane and issue join material without
// asking a live node.
//
// Only the cluster CA is managed here for now. etcd, front-proxy and the
// service-account keys are still generated on the first control plane, and a
// joining control plane fetches them with a kubeadm certificate key; injecting
// the full PKI is the direction of travel.
package pki

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

const (
	// SecretSuffix is appended to the cluster name to name the CA Secret:
	// <cluster>-ca, the Cluster API convention.
	SecretSuffix = "ca"

	// TLSCrtKey and TLSKeyKey are the Secret data keys, and match what the
	// Cluster API utilities (and the Vates bootstrap provider) expect.
	TLSCrtKey = "tls.crt"
	TLSKeyKey = "tls.key"

	// caCommonName is kubeadm's own CA subject common name. Keeping it makes an
	// injected CA indistinguishable from a generated one on the node.
	caCommonName = "kubernetes"

	// caValidity is how long the CA is valid: kubeadm's ten years.
	caValidity = 10 * 365 * 24 * time.Hour

	rsaKeyBits = 2048
)

// ClusterCA is the cluster certificate authority, as PEM. The key is the one
// secret in the whole cluster: it must never reach a node that is not a control
// plane, and never a worker.
type ClusterCA struct {
	Cert []byte
	Key  []byte
}

// GenerateClusterCA mints a fresh self-signed cluster CA.
func GenerateClusterCA() (*ClusterCA, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return nil, fmt.Errorf("pki: generating the CA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("pki: serial number: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: caCommonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: signing the CA certificate: %w", err)
	}

	return &ClusterCA{
		Cert: pemEncode("CERTIFICATE", der),
		Key:  pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)),
	}, nil
}

// SecretData renders the CA as the data of the <cluster>-ca Secret.
func (c *ClusterCA) SecretData() map[string][]byte {
	return map[string][]byte{
		TLSCrtKey: c.Cert,
		TLSKeyKey: c.Key,
	}
}

// FromSecretData rebuilds the CA from a <cluster>-ca Secret's data.
func FromSecretData(data map[string][]byte) (*ClusterCA, error) {
	for _, k := range []string{TLSCrtKey, TLSKeyKey} {
		if len(data[k]) == 0 {
			return nil, fmt.Errorf("pki: the cluster CA Secret has no %q", k)
		}
	}
	return &ClusterCA{Cert: data[TLSCrtKey], Key: data[TLSKeyKey]}, nil
}

func pemEncode(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
