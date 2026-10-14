package controller

import (
	"fmt"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// machines indexes control plane machines the way reconcileMachines does: by
// the numeric suffix of their name, "<control plane>-<index>".
func machines(indices ...int) map[int]clusterv1.Machine {
	out := make(map[int]clusterv1.Machine, len(indices))
	for _, i := range indices {
		out[i] = clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cp-%d", i)},
		}
	}
	return out
}

func TestPendingIndices(t *testing.T) {
	tests := []struct {
		name        string
		desired     int
		live        map[int]clusterv1.Machine
		terminating map[int]clusterv1.Machine
		want        []int
	}{
		{
			name:    "creates every index of a fresh control plane",
			desired: 3,
			want:    []int{0, 1, 2},
		},
		{
			name:    "creates nothing when every index is occupied",
			desired: 3,
			live:    machines(0, 1, 2),
			want:    nil,
		},
		{
			name:    "creates only the missing index",
			desired: 3,
			live:    machines(0, 2),
			want:    []int{1},
		},
		{
			name:        "leaves an index whose Machine is terminating alone",
			desired:     3,
			live:        machines(0, 2),
			terminating: machines(1),
			want:        nil,
		},
		{
			name:        "a terminating index does not hide another missing one",
			desired:     3,
			live:        machines(2),
			terminating: machines(1),
			want:        []int{0},
		},
		{
			name:    "creates nothing beyond the desired count",
			desired: 2,
			live:    machines(0),
			want:    []int{1},
		},
		{
			name:    "creates nothing for a zero replica control plane",
			desired: 0,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pendingIndices(tt.desired, tt.live, tt.terminating)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pendingIndices(desired=%d, live=%v, terminating=%v) = %v, want %v",
					tt.desired, tt.live, tt.terminating, got, tt.want)
			}
		})
	}
}

func TestDepartingNames(t *testing.T) {
	tests := []struct {
		name        string
		desired     int
		live        map[int]clusterv1.Machine
		terminating map[int]clusterv1.Machine
		want        []string
	}{
		{
			name:    "nothing leaves a steady control plane",
			desired: 3,
			live:    machines(0, 1, 2),
			want:    []string{},
		},
		{
			name:        "a terminating machine is leaving",
			desired:     3,
			live:        machines(0, 2),
			terminating: machines(1),
			want:        []string{"cp-1"},
		},
		{
			name:    "a scale-down leaves the highest indices",
			desired: 1,
			live:    machines(0, 1, 2),
			want:    []string{"cp-1", "cp-2"},
		},
		{
			name:        "a scale-down and a terminating machine are both leaving",
			desired:     1,
			live:        machines(0, 1),
			terminating: machines(3),
			want:        []string{"cp-1", "cp-3"},
		},
		{
			name:        "names from both sources come out sorted",
			desired:     2,
			live:        machines(0, 2),
			terminating: machines(1),
			want:        []string{"cp-1", "cp-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := departingNames(tt.live, tt.terminating, tt.desired)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("departingNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEtcdHost(t *testing.T) {
	tests := []struct {
		name      string
		desired   int
		live      map[int]clusterv1.Machine
		departing []string
		want      string
		wantOK    bool
	}{
		{
			name:    "picks a control plane that is staying",
			desired: 3,
			live:    machines(0, 1, 2),
			want:    "cp-0",
			wantOK:  true,
		},
		{
			name:      "never picks the machine being removed",
			desired:   3,
			live:      machines(0, 1, 2),
			departing: []string{"cp-0", "cp-1"},
			want:      "cp-2",
			wantOK:    true,
		},
		{
			name:      "never picks a machine the scale-down is about to delete",
			desired:   1,
			live:      machines(0, 1, 2),
			departing: []string{"cp-1", "cp-2"},
			want:      "cp-0",
			wantOK:    true,
		},
		{
			name:      "has no host when every control plane is leaving",
			desired:   3,
			live:      machines(0, 1, 2),
			departing: []string{"cp-0", "cp-1", "cp-2"},
			wantOK:    false,
		},
		{
			name:      "has no host when the index is not filled yet",
			desired:   3,
			live:      machines(1, 2),
			departing: []string{"cp-1"},
			want:      "cp-2",
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := etcdHost(tt.live, tt.departing, tt.desired)
			if ok != tt.wantOK {
				t.Fatalf("etcdHost() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("etcdHost() = %q, want %q", got, tt.want)
			}
		})
	}
}
