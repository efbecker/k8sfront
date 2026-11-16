// Package upgrade plans Kubernetes version upgrades (phase 5): the chain of
// minors to walk (never skipping one, per the version skew policy) and the
// pre-checks shown before the operator starts a cluster.upgrade job.
package upgrade

import (
	"context"
	"fmt"
	"sort"

	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
)

// Target is a version the cluster can be upgraded to, with the hops the job
// walks to reach it (one per minor; the last hop is the target).
type Target struct {
	Version string   `json:"version"`
	Kind    string   `json:"kind"` // patch | minor
	Hops    []string `json:"hops"`
	Latest  bool     `json:"latest"`
}

// LatestPatchFunc returns the newest stable patch of a minor line, or an
// error when that line has no stable release (yet).
type LatestPatchFunc func(ctx context.Context, major, minor int) (k8sver.Version, error)

// MaxMinorHops bounds how far ahead the plan looks.
const MaxMinorHops = 6

// Targets computes the reachable versions from cur: a newer patch of the same
// minor, then the latest patch of each following minor (each one requires
// the previous). kubeadm can go from any patch of 1.N straight to 1.N+1.x, so
// a minor hop never needs the patch upgrade first.
func Targets(ctx context.Context, cur k8sver.Version, latest LatestPatchFunc) ([]Target, error) {
	var out []Target
	if p, err := latest(ctx, cur.Major, cur.Minor); err == nil && p.Compare(cur) > 0 {
		out = append(out, Target{Version: p.String(), Kind: "patch", Hops: []string{p.String()}})
	} else if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var hops []string
	for m := cur.Minor + 1; m <= cur.Minor+MaxMinorHops; m++ {
		v, err := latest(ctx, cur.Major, m)
		if err != nil || v.Minor != m {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			break
		}
		hops = append(hops, v.String())
		out = append(out, Target{Version: v.String(), Kind: "minor", Hops: append([]string(nil), hops...)})
	}
	if len(out) > 0 {
		out[len(out)-1].Latest = true
	}
	return out, nil
}

// Resolve finds target among the reachable targets.
func Resolve(targets []Target, version string) (*Target, error) {
	want, err := k8sver.Parse(version)
	if err != nil {
		return nil, err
	}
	for i := range targets {
		if targets[i].Version == want.String() {
			return &targets[i], nil
		}
	}
	return nil, fmt.Errorf("%s não é um destino válido a partir da versão atual (só são aceitas as versões do plano, sem pular minors)", want)
}

// MinVersion returns the lowest version in vs (used to find the oldest kubelet).
func MinVersion(vs []string) (k8sver.Version, bool) {
	var parsed []k8sver.Version
	for _, s := range vs {
		if v, err := k8sver.Parse(s); err == nil {
			parsed = append(parsed, v)
		}
	}
	if len(parsed) == 0 {
		return k8sver.Version{}, false
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Compare(parsed[j]) < 0 })
	return parsed[0], true
}
