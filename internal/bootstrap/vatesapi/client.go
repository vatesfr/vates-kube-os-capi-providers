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

// Package vatesapi is a client for a Vates Kube OS node's management API
// (gRPC over mutual TLS on :50000). The provider uses it to fetch the cluster
// kubeconfig and fresh join material from a live control plane.
package vatesapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/yaml.v3"

	apiv1 "github.com/vatesfr/vates-kube-os/proto/vates/api/v1"

	"github.com/vatesfr/vates-kube-os-capi-providers/internal/bootstrap/pki"
)

// Client is a connection to one node's management API.
type Client struct {
	conn *grpc.ClientConn
	api  apiv1.VatesAPIClient
}

// Dial connects to the management API at addr (host:port), authenticating with
// the operator CA and the client certificate it signed. The server certificate
// is verified against the same CA, so no cluster CA is needed to talk.
func Dial(addr string, ca *pki.OperatorCA) (*Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.Cert) {
		return nil, fmt.Errorf("vatesapi: the operator CA holds no certificate")
	}
	cert, err := tls.X509KeyPair(ca.ClientCert, ca.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("vatesapi: building the client key pair: %w", err)
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf)))
	if err != nil {
		return nil, fmt.Errorf("vatesapi: dialing %s: %w", addr, err)
	}
	return &Client{conn: conn, api: apiv1.NewVatesAPIClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Kubeconfig returns the cluster's admin kubeconfig.
func (c *Client) Kubeconfig(ctx context.Context) ([]byte, error) {
	resp, err := c.api.GetKubeconfig(ctx, &apiv1.GetKubeconfigRequest{})
	if err != nil {
		return nil, fmt.Errorf("vatesapi: GetKubeconfig: %w", err)
	}
	return resp.Kubeconfig, nil
}

// JoinMaterial is what a joining machine needs: a bootstrap token, the
// certificate key a joining control plane fetches the shared certificates with,
// and the CA hash it verifies the cluster with.
type JoinMaterial struct {
	Token          string
	CertificateKey string
	CACertHash     string
}

// GetJoinMaterial asks a live control plane for fresh join credentials.
func (c *Client) GetJoinMaterial(ctx context.Context) (JoinMaterial, error) {
	resp, err := c.api.GetJoinMaterial(ctx, &apiv1.GetJoinMaterialRequest{})
	if err != nil {
		return JoinMaterial{}, fmt.Errorf("vatesapi: GetJoinMaterial: %w", err)
	}
	return JoinMaterial{
		Token:          resp.Token,
		CertificateKey: resp.CertificateKey,
		CACertHash:     resp.CaCertHash,
	}, nil
}

// ClusterCAFromKubeconfig extracts the cluster CA certificate (PEM) from a
// kubeconfig's certificate-authority-data. It is the CA a joining machine needs
// on its drive, and it is public: the private key never leaves the control
// planes.
func ClusterCAFromKubeconfig(raw []byte) (string, error) {
	var kc struct {
		Clusters []struct {
			Cluster struct {
				CAData string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if err := yaml.Unmarshal(raw, &kc); err != nil {
		return "", fmt.Errorf("vatesapi: parsing the kubeconfig: %w", err)
	}
	if len(kc.Clusters) == 0 || kc.Clusters[0].Cluster.CAData == "" {
		return "", fmt.Errorf("vatesapi: the kubeconfig carries no certificate-authority-data")
	}
	ca, err := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CAData)
	if err != nil {
		return "", fmt.Errorf("vatesapi: decoding certificate-authority-data: %w", err)
	}
	return string(ca), nil
}
