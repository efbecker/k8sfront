package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNamespaceInventory(t *testing.T) {
	om := func(ns, name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: ns, Name: name} }
	owned := metav1.ObjectMeta{Namespace: "shop", Name: "web-abc", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-1"}}}
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "empty"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&appsv1.Deployment{ObjectMeta: om("shop", "web")},
		&corev1.Pod{ObjectMeta: owned},
		&corev1.Service{ObjectMeta: om("shop", "web")},
		&corev1.PersistentVolumeClaim{ObjectMeta: om("shop", "data")},
		&corev1.ConfigMap{ObjectMeta: om("shop", "kube-root-ca.crt")},
		&corev1.ConfigMap{ObjectMeta: om("empty", "kube-root-ca.crt")},
		&corev1.ServiceAccount{ObjectMeta: om("empty", "default")},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "sh.helm.release.v1.redis.v2", Labels: map[string]string{"owner": "helm", "name": "redis"}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "sh.helm.release.v1.redis.v1", Labels: map[string]string{"owner": "helm", "name": "redis"}}},
		&corev1.Secret{ObjectMeta: om("shop", "web-registry")},
	)
	all, err := NamespaceInventory(context.Background(), cs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]NamespaceUsage{}
	for _, u := range all {
		by[u.Name] = u
	}
	shop := by["shop"]
	want := map[string]int{"Deployment": 1, "Service": 1, "PersistentVolumeClaim": 1, "HelmRelease": 1, "Secret": 1}
	for k, v := range want {
		if shop.Counts[k] != v {
			t.Fatalf("shop counts %v", shop.Counts)
		}
	}
	if shop.Pods != 1 || len(shop.Items) != 5 || shop.Counts["Pod"] != 0 || shop.Counts["ConfigMap"] != 0 {
		t.Fatalf("shop %+v", shop)
	}
	if len(by["empty"].Items) != 0 || by["empty"].System || !by["kube-system"].System {
		t.Fatalf("empty/system: %+v %+v", by["empty"], by["kube-system"])
	}
	one, err := NamespaceInventory(context.Background(), cs, nil, "empty")
	if err != nil || len(one) != 1 || one[0].Name != "empty" {
		t.Fatalf("single: %+v %v", one, err)
	}
}

func TestReadyzMessage(t *testing.T) {
	raw := errors.New(`an error on the server ("[+]ping ok\n[-]etcd failed: reason withheld\n[-]etcd-readiness failed: reason withheld\n[+]log ok\nreadyz check failed") has prevented the request from succeeding`)
	got := ReadyzMessage(raw)
	if !strings.Contains(got, "(etcd, etcd-readiness)") || strings.Contains(got, "ping") {
		t.Fatalf("got %q", got)
	}
	if got := ReadyzMessage(errors.New("dial tcp 10.0.0.1:6443: connect: connection refused")); !strings.Contains(got, "connection refused") {
		t.Fatalf("got %q", got)
	}
}
