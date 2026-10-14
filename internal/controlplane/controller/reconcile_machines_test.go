package controller

import (
	"reflect"
	"testing"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func TestPendingIndices(t *testing.T) {
	live := func(indices ...int) map[int]clusterv1.Machine {
		out := make(map[int]clusterv1.Machine, len(indices))
		for _, i := range indices {
			out[i] = clusterv1.Machine{}
		}
		return out
	}
	terminating := func(indices ...int) map[int]bool {
		out := make(map[int]bool, len(indices))
		for _, i := range indices {
			out[i] = true
		}
		return out
	}

	tests := []struct {
		name        string
		desired     int
		live        map[int]clusterv1.Machine
		terminating map[int]bool
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
			live:    live(0, 1, 2),
			want:    nil,
		},
		{
			name:    "creates only the missing index",
			desired: 3,
			live:    live(0, 2),
			want:    []int{1},
		},
		{
			name:        "leaves an index whose Machine is terminating alone",
			desired:     3,
			live:        live(0, 2),
			terminating: terminating(1),
			want:        nil,
		},
		{
			name:        "a terminating index does not hide another missing one",
			desired:     3,
			live:        live(2),
			terminating: terminating(1),
			want:        []int{0},
		},
		{
			name:    "creates nothing beyond the desired count",
			desired: 2,
			live:    live(0),
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
