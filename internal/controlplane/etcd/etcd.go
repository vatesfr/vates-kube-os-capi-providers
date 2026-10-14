// Package etcd removes a control plane machine's etcd member from the cluster
// it belonged to.
//
// A control plane provider that replaces a machine has to take the machine's
// etcd member with it. Adding the replacement's member before removing the old
// one raises the quorum: a three-member cluster with one member down goes from
// needing two votes to needing three, which buys no fault tolerance and leaves
// the cluster one failure away from losing its data for good. Removing first
// keeps the quorum where it was, and the replacement is added into the room the
// removal made.
//
// The removal runs through the etcd static pod already on a live control plane:
// it carries etcdctl and the member's own certificates, so nothing has to be
// scheduled, no image has to be pulled, and no etcd client has to be built
// here. The provider reaches that pod with the cluster's admin kubeconfig,
// which it already owns. Any control plane can remove any member, so the pod
// that is used is never the one being removed.
package etcd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Member is one member of the cluster's etcd.
type Member struct {
	ID         uint64
	Name       string
	PeerURLs   []string
	ClientURLs []string
	// Healthy reports whether the member answered when the cluster was probed.
	Healthy bool
}

// Client reaches etcd through the static pod that runs it on a live control
// plane.
type Client struct {
	rest      *rest.Config
	cs        kubernetes.Interface
	namespace string
	pod       string
	container string
}

// New returns a client that speaks to etcd through the etcd static pod running
// on the node called nodeName.
func New(cfg *rest.Config, nodeName string) (*Client, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("etcd: building the workload cluster client: %w", err)
	}
	return &Client{
		rest:      cfg,
		cs:        cs,
		namespace: metav1.NamespaceSystem,
		pod:       "etcd-" + nodeName,
		container: "etcd",
	}, nil
}

// Members lists the cluster's members and probes each one.
//
// The probe is part of the listing rather than a separate call because every
// caller needs both, and because a member list without health cannot answer the
// only question that matters here: whether a removal would cost the cluster its
// quorum.
func (c *Client) Members(ctx context.Context) ([]Member, error) {
	members, err := c.memberList(ctx)
	if err != nil {
		return nil, err
	}
	healthy, err := c.healthyClientURLs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range members {
		for _, url := range members[i].ClientURLs {
			if healthy[url] {
				members[i].Healthy = true
				break
			}
		}
	}
	return members, nil
}

// RemoveMemberNamed removes the member named after a node, reporting whether
// there was one to remove.
//
// It is the whole operation a control plane provider needs: find the member a
// machine left behind, check that taking it away cannot cost the cluster its
// quorum, and remove it. A member that is already gone is not an error: the
// caller retries, and the second attempt has to be a no-op.
func (c *Client) RemoveMemberNamed(ctx context.Context, nodeName string) (bool, error) {
	members, err := c.Members(ctx)
	if err != nil {
		return false, err
	}
	var target *Member
	for i := range members {
		if members[i].Name == nodeName {
			target = &members[i]
			break
		}
	}
	if target == nil {
		return false, nil
	}
	if ok, reason := SafeToRemove(members, target.Name); !ok {
		return false, fmt.Errorf("etcd: refusing to remove the member for %s: %s", nodeName, reason)
	}
	return true, c.remove(ctx, target.ID)
}

// SafeToRemove reports whether removing the member called name leaves the
// cluster able to field requests, and why not when it does not.
//
// etcd needs a majority of its members to agree before it changes anything,
// membership included. Removing a member lowers both the count and the
// majority, so the risk is the opposite of the obvious one: removing the only
// member that is down is safe, and is exactly what replacing a machine needs,
// while removing a healthy member while others are already down takes the
// survivors below the majority they need. A cluster that cannot agree cannot be
// repaired either, and that is what makes the mistake permanent.
func SafeToRemove(members []Member, name string) (bool, string) {
	if len(members) < 2 {
		return false, "it is the cluster's only member: removing it would take the data with it"
	}
	remaining := len(members) - 1
	quorum := remaining/2 + 1
	healthy := 0
	for _, m := range members {
		if m.Name != name && m.Healthy {
			healthy++
		}
	}
	if healthy < quorum {
		return false, fmt.Sprintf(
			"only %d of the %d members left would answer, and etcd needs %d to agree",
			healthy, remaining, quorum)
	}
	return true, ""
}

// memberList reads the cluster's membership.
func (c *Client) memberList(ctx context.Context) ([]Member, error) {
	out, err := c.etcdctl(ctx, "member", "list", "-w", "json")
	if err != nil {
		return nil, err
	}
	return parseMembers(out)
}

// remove removes one member by ID.
func (c *Client) remove(ctx context.Context, id uint64) error {
	if _, err := c.etcdctl(ctx, "member", "remove", memberIDArg(id)); err != nil {
		return fmt.Errorf("etcd: removing member %s: %w", memberIDArg(id), err)
	}
	return nil
}

// memberIDArg renders a member ID the way etcdctl wants it: in hex, not in the
// decimal that `member list -w json` reports. Passing the decimal fails with
// "expecting ID in Hex", which is the mistake this names.
func memberIDArg(id uint64) string {
	return strconv.FormatUint(id, 16)
}

// healthyClientURLs probes every member of the cluster and returns the ones
// that answered.
//
// An unhealthy member makes etcdctl exit non-zero, so an error is not fatal
// here: the output is the answer, and an empty one is the answer that matters
// most -- it means nothing could be probed, which is not a state to remove a
// member from.
func (c *Client) healthyClientURLs(ctx context.Context) (map[string]bool, error) {
	out, err := c.etcdctl(ctx, "endpoint", "health", "--cluster")
	healthy := parseHealth(out)
	if len(healthy) == 0 && err != nil {
		return nil, err
	}
	return healthy, nil
}

// parseMembers reads `etcdctl member list -w json`.
func parseMembers(out []byte) ([]Member, error) {
	var doc struct {
		Members []struct {
			ID         uint64   `json:"ID"`
			Name       string   `json:"name"`
			PeerURLs   []string `json:"peerURLs"`
			ClientURLs []string `json:"clientURLs"`
		} `json:"members"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("etcd: reading the member list: %w", err)
	}
	members := make([]Member, 0, len(doc.Members))
	for _, m := range doc.Members {
		members = append(members, Member{
			ID:         m.ID,
			Name:       m.Name,
			PeerURLs:   m.PeerURLs,
			ClientURLs: m.ClientURLs,
		})
	}
	return members, nil
}

// parseHealth reads `etcdctl endpoint health --cluster`, whose lines are
// "<client URL> is healthy: ..." or "<client URL> is unhealthy: ...".
func parseHealth(out []byte) map[string]bool {
	healthy := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		url, verdict, ok := strings.Cut(strings.TrimSpace(line), " is ")
		if !ok || !strings.HasPrefix(verdict, "healthy") {
			continue
		}
		healthy[url] = true
	}
	return healthy
}

// etcdctl runs etcdctl in the etcd pod, with the connection flags the pod
// itself runs etcd with.
func (c *Client) etcdctl(ctx context.Context, args ...string) ([]byte, error) {
	connArgs, err := c.connectionArgs(ctx)
	if err != nil {
		return nil, err
	}
	cmd := append(append([]string{"etcdctl"}, connArgs...), args...)

	req := c.cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(c.pod).
		Namespace(c.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: c.container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.rest, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("etcd: reaching %s/%s: %w", c.namespace, c.pod, err)
	}

	var stdout, stderr bytes.Buffer
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if streamErr != nil {
		// stdout still carries the answer when etcdctl failed on a member
		// rather than on the call; the caller decides what to do with it.
		return stdout.Bytes(), fmt.Errorf("etcd: etcdctl %s: %w: %s",
			strings.Join(args, " "), streamErr, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// connectionArgs are the flags etcdctl needs to reach this node's etcd with the
// identity etcd itself runs with. They are read from the pod's own command
// line, so a cluster whose certificates live somewhere else still works.
func (c *Client) connectionArgs(ctx context.Context) ([]string, error) {
	pod, err := c.cs.CoreV1().Pods(c.namespace).Get(ctx, c.pod, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("etcd: reading the etcd pod %s/%s: %w", c.namespace, c.pod, err)
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != c.container {
			continue
		}
		command := append(append([]string{}, container.Command...), container.Args...)
		return etcdctlArgs(command)
	}
	return nil, fmt.Errorf("etcd: %s/%s has no %q container", c.namespace, c.pod, c.container)
}

// etcdctlArgs maps the flags etcd is started with to the ones etcdctl takes.
//
// The names differ, and one of them changes meaning: etcd listens on several
// client URLs while etcdctl wants one endpoint. The loopback is preferred --
// the call runs inside the pod, so it is the shortest path and the only one
// that does not depend on the node's own address, which is exactly what is in
// question when a machine is being replaced.
func etcdctlArgs(command []string) ([]string, error) {
	// Both spellings are accepted: kubeadm writes "--flag=value", and etcd
	// itself accepts the separated form, so a manifest that uses it still works.
	get := func(flag string) string {
		prefix := "--" + flag
		for i, arg := range command {
			if arg == prefix && i+1 < len(command) {
				return command[i+1]
			}
			if value, ok := strings.CutPrefix(arg, prefix+"="); ok {
				return value
			}
		}
		return ""
	}

	endpoint := ""
	for _, url := range strings.Split(get("listen-client-urls"), ",") {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		if endpoint == "" || strings.Contains(url, "127.0.0.1") {
			endpoint = url
		}
		if strings.Contains(url, "127.0.0.1") {
			break
		}
	}

	args := []string{
		"--endpoints=" + endpoint,
		"--cacert=" + get("trusted-ca-file"),
		"--cert=" + get("cert-file"),
		"--key=" + get("key-file"),
	}
	for _, arg := range args {
		if strings.HasSuffix(arg, "=") {
			return nil, fmt.Errorf("etcd: the etcd pod does not name %s, so etcdctl cannot reach it",
				strings.SplitN(arg, "=", 2)[0])
		}
	}
	return args, nil
}
