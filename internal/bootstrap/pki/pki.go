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

// Package pki mints the operator certificate authority a Vates Kube OS cluster
// uses for its management API, and the client certificate the provider presents
// to it.
//
// The node generates its own CLUSTER CA (kubeadm does, on the bootstrapping
// control plane) and never hands out its private key. So the provider cannot use
// the cluster CA to authenticate. Instead it generates a CA of its own, has the
// certificate AND its key written on the bootstrapping control plane's drive (the
// node mints its :50000 server certificate from it and trusts the clients it
// signs), and keeps the key. One authority covers both directions of mutual TLS,
// which is what lets the provider call GetJoinMaterial/GetKubeconfig without ever
// holding the cluster CA.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// OperatorCA is a generated operator authority and the client credential the
// provider uses against the management API. All four values are PEM.
type OperatorCA struct {
	Cert       []byte
	Key        []byte
	ClientCert []byte
	ClientKey  []byte
}

// GenerateOperatorCA mints a fresh operator CA (ECDSA P-256) and a client
// certificate signed by it. The client carries organization vates:admin, the
// identity the management API admits.
func GenerateOperatorCA(commonName string) (*OperatorCA, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generating the CA key: %w", err)
	}
	caTmpl, err := newTemplate("vates-api-ca-"+commonName, []string{"vates:admin"}, true, []x509.ExtKeyUsage{})
	if err != nil {
		return nil, err
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: signing the CA certificate: %w", err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generating the client key: %w", err)
	}
	clientTmpl, err := newTemplate("vates-capi-"+commonName, []string{"vates:admin"}, false, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if err != nil {
		return nil, err
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caTmpl, &clientKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: signing the client certificate: %w", err)
	}

	caKeyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: encoding the CA key: %w", err)
	}
	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		return nil, fmt.Errorf("pki: encoding the client key: %w", err)
	}

	return &OperatorCA{
		Cert:       pemEncode("CERTIFICATE", caDER),
		Key:        pemEncode("EC PRIVATE KEY", caKeyDER),
		ClientCert: pemEncode("CERTIFICATE", clientDER),
		ClientKey:  pemEncode("EC PRIVATE KEY", clientKeyDER),
	}, nil
}

// SecretData is the operator CA as the data of a management-cluster Secret.
func (o *OperatorCA) SecretData() map[string][]byte {
	return map[string][]byte{
		"ca.crt":     o.Cert,
		"ca.key":     o.Key,
		"client.crt": o.ClientCert,
		"client.key": o.ClientKey,
	}
}

// FromSecretData rebuilds an OperatorCA from a Secret's data.
func FromSecretData(data map[string][]byte) (*OperatorCA, error) {
	for _, k := range []string{"ca.crt", "ca.key", "client.crt", "client.key"} {
		if len(data[k]) == 0 {
			return nil, fmt.Errorf("pki: the operator CA Secret has no %q", k)
		}
	}
	return &OperatorCA{
		Cert:       data["ca.crt"],
		Key:        data["ca.key"],
		ClientCert: data["client.crt"],
		ClientKey:  data["client.key"],
	}, nil
}

func newTemplate(cn string, org []string, isCA bool, eku []x509.ExtKeyUsage) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("pki: serial number: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: org},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
	}
	if isCA {
		tmpl.IsCA = true
		tmpl.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	return tmpl, nil
}

func pemEncode(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
