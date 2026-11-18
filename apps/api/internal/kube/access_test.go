package kube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListAccess(t *testing.T) {
	ready := true
	lb := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "teste", Name: "nginx-teste"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Selector: map[string]string{"app": "nginx-teste"},
			Ports: []corev1.ServicePort{{Port: 80, NodePort: 30080, Protocol: corev1.ProtocolTCP}}}}
	internal := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "teste", Name: "db"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 5432}}}}
	web := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "teste", Name: "web"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 8080}}}}
	prefix := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "teste", Name: "web"},
		Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"web.example.com"}}},
			Rules: []networkingv1.IngressRule{{Host: "web.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{Path: "/app", PathType: &prefix, Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Number: 8080}}}}}}}}}}}
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "teste", Name: "nginx-teste-x", Labels: map[string]string{"kubernetes.io/service-name": "nginx-teste"}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.244.1.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
			{Addresses: []string{"10.244.1.3"}}}}
	cs := fake.NewClientset(node("cp1", true, true), node("w2", true, false), node("w3", false, false), lb, internal, web, ing, slice)

	acc, err := ListAccess(context.Background(), cs, "teste", LoadBalancerInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.Services) != 2 {
		t.Fatalf("services = %+v (ClusterIP without Ingress must be left out)", acc.Services)
	}
	n := acc.Services[0]
	if n.Name != "nginx-teste" || !n.Pending || n.Endpoints != 2 || acc.LoadBalancer.Installed {
		t.Fatalf("nginx = %+v, lb = %+v", n, acc.LoadBalancer)
	}
	// Ready nodes only, workers first.
	if len(n.Links) != 2 || n.Links[0].URL != "http://10.0.0.2:30080/" || n.Links[1].Node != "cp1" || n.Links[0].Kind != AccessNodePort {
		t.Fatalf("nodeport links = %+v", n.Links)
	}
	w := acc.Services[1]
	if w.Name != "web" || len(w.Links) != 1 || w.Links[0].URL != "https://web.example.com/app" || w.Endpoints != 0 {
		t.Fatalf("web = %+v", w)
	}

	lb.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.168.1.200"}}
	if _, err := cs.CoreV1().Services("teste").UpdateStatus(context.Background(), lb, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	acc, _ = ListAccess(context.Background(), cs, "teste", LoadBalancerInfo{Provider: "metallb"})
	n = acc.Services[0]
	if n.Pending || n.Links[0].URL != "http://192.168.1.200/" || n.Links[0].Kind != AccessLoadBalancer {
		t.Fatalf("lb links = %+v", n.Links)
	}
	if !acc.HasLink("teste", "nginx-teste", "http://192.168.1.200/") || acc.HasLink("teste", "nginx-teste", "http://169.254.169.254/") ||
		acc.HasLink("teste", "web", "http://192.168.1.200/") {
		t.Fatal("HasLink must only accept the Service's own links")
	}
}

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer srv.Close()
	if r := Probe(context.Background(), srv.URL+"/"); !r.OK || r.Status != http.StatusFound {
		t.Fatalf("redirect must count as reachable and not be followed: %+v", r)
	}
	if r := Probe(context.Background(), srv.URL+"/boom"); r.OK || r.Status != http.StatusBadGateway || r.Error == "" {
		t.Fatalf("5xx = %+v", r)
	}
	addr := srv.Listener.Addr().String()
	srv.Close()
	if r := Probe(context.Background(), "http://"+addr+"/"); r.OK || r.Error == "" {
		t.Fatalf("closed port = %+v", r)
	}
	if r := Probe(context.Background(), "file:///etc/passwd"); r.OK || r.Error != "URL inválida" {
		t.Fatalf("scheme = %+v", r)
	}
}
