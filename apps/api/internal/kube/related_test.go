package kube

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRelatedObjects(t *testing.T) {
	dep := func(name string, labels map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "teste"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}}
	}
	svc := func(name string, sel map[string]string) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "teste"}, Spec: corev1.ServiceSpec{Selector: sel}}
	}
	ing := func(name string, backends ...string) *networkingv1.Ingress {
		var paths []networkingv1.HTTPIngressPath
		for _, b := range backends {
			paths = append(paths, networkingv1.HTTPIngressPath{Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: b}}})
		}
		return &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "teste"},
			Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths}}}}}}
	}
	cs := fake.NewSimpleClientset(
		dep("nginx", map[string]string{"app": "nginx", "tier": "web"}),
		dep("api", map[string]string{"app": "api", "tier": "web"}),
		svc("nginx", map[string]string{"app": "nginx"}),
		svc("web", map[string]string{"tier": "web"}), // shared by both
		svc("ext", nil),                              // no selector
		ing("nginx", "nginx"),
		ing("mixed", "nginx", "web"),
	)
	got, err := RelatedObjects(context.Background(), cs, "teste", "Deployment", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Ref{"v1", "Service", "teste", "nginx"}) || got[1] != (Ref{"networking.k8s.io/v1", "Ingress", "teste", "nginx"}) {
		t.Fatalf("related = %+v", got)
	}
	if got, err := RelatedObjects(context.Background(), cs, "teste", "Deployment", "gone"); err != nil || len(got) != 0 {
		t.Fatalf("missing workload: %v %v", got, err)
	}
}
