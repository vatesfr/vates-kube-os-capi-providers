package etcd

import (
	"reflect"
	"strings"
	"testing"
)

// etcdCommand is the command line of the etcd static pod as kubeadm writes it,
// copied from a live cluster. It is the input etcdctlArgs has to read, so it is
// the input the test uses.
func etcdCommand() []string {
	return []string{
		"etcd",
		"--advertise-client-urls=https://10.30.1.240:2379",
		"--cert-file=/etc/kubernetes/pki/etcd/server.crt",
		"--client-cert-auth=true",
		"--data-dir=/var/lib/etcd",
		"--feature-gates=InitialCorruptCheck=true",
		"--initial-advertise-peer-urls=https://10.30.1.240:2380",
		"--initial-cluster=vates-mp-0=https://10.30.1.240:2380",
		"--key-file=/etc/kubernetes/pki/etcd/server.key",
		"--listen-client-urls=https://127.0.0.1:2379,https://10.30.1.240:2379",
		"--listen-metrics-urls=http://127.0.0.1:2381",
		"--listen-peer-urls=https://10.30.1.240:2380",
		"--name=vates-mp-0",
		"--peer-cert-file=/etc/kubernetes/pki/etcd/peer.crt",
		"--peer-client-cert-auth=true",
		"--peer-key-file=/etc/kubernetes/pki/etcd/peer.key",
		"--peer-trusted-ca-file=/etc/kubernetes/pki/etcd/ca.crt",
		"--snapshot-count=10000",
		"--trusted-ca-file=/etc/kubernetes/pki/etcd/ca.crt",
		"--watch-progress-notify-interval=5s",
	}
}

func TestEtcdctlArgs(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		want    []string
		wantErr string
	}{
		{
			name:    "reads the flags kubeadm writes, preferring the loopback endpoint",
			command: etcdCommand(),
			want: []string{
				"--endpoints=https://127.0.0.1:2379",
				"--cacert=/etc/kubernetes/pki/etcd/ca.crt",
				"--cert=/etc/kubernetes/pki/etcd/server.crt",
				"--key=/etc/kubernetes/pki/etcd/server.key",
			},
		},
		{
			name: "takes the only endpoint when there is no loopback",
			command: []string{
				"--listen-client-urls=https://10.30.1.240:2379",
				"--trusted-ca-file=/ca.crt",
				"--cert-file=/cert.crt",
				"--key-file=/key.key",
			},
			want: []string{
				"--endpoints=https://10.30.1.240:2379",
				"--cacert=/ca.crt",
				"--cert=/cert.crt",
				"--key=/key.key",
			},
		},
		{
			name: "accepts the separated flag spelling",
			command: []string{
				"--listen-client-urls", "https://127.0.0.1:2379",
				"--trusted-ca-file", "/ca.crt",
				"--cert-file", "/cert.crt",
				"--key-file", "/key.key",
			},
			want: []string{
				"--endpoints=https://127.0.0.1:2379",
				"--cacert=/ca.crt",
				"--cert=/cert.crt",
				"--key=/key.key",
			},
		},
		{
			name: "refuses when the pod does not name the certificates",
			command: []string{
				"--listen-client-urls=https://127.0.0.1:2379",
			},
			wantErr: "does not name --cacert",
		},
		{
			name:    "refuses an empty command",
			command: nil,
			wantErr: "does not name --endpoints",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := etcdctlArgs(tt.command)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("etcdctlArgs() error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("etcdctlArgs() unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("etcdctlArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// memberListJSON is `etcdctl member list -w json` from a live three-control-plane
// cluster.
const memberListJSON = `{"header":{"cluster_id":4923970448933968357,"member_id":6820929080627452059,` +
	`"raft_term":2},"members":[` +
	`{"ID":3346114166147024158,"name":"vates-mp-1","peerURLs":["https://10.30.1.249:2380"],` +
	`"clientURLs":["https://10.30.1.249:2379"]},` +
	`{"ID":6820929080627452059,"name":"vates-mp-0","peerURLs":["https://10.30.1.240:2380"],` +
	`"clientURLs":["https://10.30.1.240:2379"]},` +
	`{"ID":7466374575268487584,"name":"vates-mp-2","peerURLs":["https://10.30.1.250:2380"],` +
	`"clientURLs":["https://10.30.1.250:2379"]}]}`

func TestParseMembers(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []Member
		wantErr bool
	}{
		{
			name: "reads the members a live cluster reports",
			in:   memberListJSON,
			want: []Member{
				{ID: 3346114166147024158, Name: "vates-mp-1", PeerURLs: []string{"https://10.30.1.249:2380"}, ClientURLs: []string{"https://10.30.1.249:2379"}},
				{ID: 6820929080627452059, Name: "vates-mp-0", PeerURLs: []string{"https://10.30.1.240:2380"}, ClientURLs: []string{"https://10.30.1.240:2379"}},
				{ID: 7466374575268487584, Name: "vates-mp-2", PeerURLs: []string{"https://10.30.1.250:2380"}, ClientURLs: []string{"https://10.30.1.250:2379"}},
			},
		},
		{
			name: "reads an empty membership",
			in:   `{"members":[]}`,
			want: []Member{},
		},
		{
			name:    "refuses anything that is not the member list",
			in:      "Error: context deadline exceeded\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMembers([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMembers() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMembers() unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseMembers() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseHealth(t *testing.T) {
	// The real shape: one line per member, healthy or not, and a member that
	// could not be reached at all may simply be missing.
	in := `https://10.30.1.240:2379 is healthy: successfully committed proposal: took = 26.093257ms
https://10.30.1.249:2379 is unhealthy: failed to commit proposal: context deadline exceeded
https://10.30.1.250:2379 is healthy: successfully committed proposal: took = 33.04884ms`

	want := map[string]bool{
		"https://10.30.1.240:2379": true,
		"https://10.30.1.250:2379": true,
	}

	got := parseHealth([]byte(in))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseHealth() = %v, want %v", got, want)
	}
}

func TestSafeToRemove(t *testing.T) {
	members := func(names ...string) []Member {
		out := make([]Member, 0, len(names))
		for _, n := range names {
			out = append(out, Member{Name: n, Healthy: true})
		}
		return out
	}
	down := func(ms []Member, names ...string) []Member {
		for i := range ms {
			for _, n := range names {
				if ms[i].Name == n {
					ms[i].Healthy = false
				}
			}
		}
		return ms
	}

	tests := []struct {
		name    string
		members []Member
		target  string
		want    bool
	}{
		{
			name:    "removes a healthy member while the others can still agree",
			members: members("cp-0", "cp-1", "cp-2"),
			target:  "cp-2",
			want:    true,
		},
		{
			name:    "removes the member that is the one down",
			members: down(members("cp-0", "cp-1", "cp-2"), "cp-2"),
			target:  "cp-2",
			want:    true,
		},
		{
			name:    "refuses to remove a healthy member while another is down",
			members: down(members("cp-0", "cp-1", "cp-2"), "cp-1"),
			target:  "cp-2",
			want:    false,
		},
		{
			name:    "refuses to remove the only member",
			members: members("cp-0"),
			target:  "cp-0",
			want:    false,
		},
		{
			name:    "refuses an empty membership",
			members: nil,
			target:  "cp-0",
			want:    false,
		},
		{
			name:    "removes from a two-member cluster while one survives",
			members: members("cp-0", "cp-1"),
			target:  "cp-1",
			want:    true,
		},
		{
			name:    "refuses when nothing answers, so the quorum cannot be judged",
			members: down(members("cp-0", "cp-1", "cp-2"), "cp-0", "cp-1", "cp-2"),
			target:  "cp-2",
			want:    false,
		},
		{
			name:    "removes from a five-member cluster while a majority survives",
			members: down(members("cp-0", "cp-1", "cp-2", "cp-3", "cp-4"), "cp-3", "cp-4"),
			target:  "cp-4",
			want:    true,
		},
		{
			name:    "refuses when two of five are down and a healthy one is targeted",
			members: down(members("cp-0", "cp-1", "cp-2", "cp-3", "cp-4"), "cp-3", "cp-4"),
			target:  "cp-2",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := SafeToRemove(tt.members, tt.target)
			if got != tt.want {
				t.Errorf("SafeToRemove() = %v (%s), want %v", got, reason, tt.want)
			}
			if got && reason != "" {
				t.Errorf("SafeToRemove() allowed the removal but gave a reason: %q", reason)
			}
			if !got && reason == "" {
				t.Errorf("SafeToRemove() refused without saying why")
			}
		})
	}
}
