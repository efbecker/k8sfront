package deploy

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// ObjectRef names one object to delete.
type ObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// FromRefs turns references into (empty) objects for Delete.
func FromRefs(refs []ObjectRef) ([]*unstructured.Unstructured, error) {
	if len(refs) == 0 {
		return nil, invalid("nenhum objeto para excluir")
	}
	if len(refs) > MaxObjects {
		return nil, invalid("no máximo %d objetos por exclusão", MaxObjects)
	}
	out := make([]*unstructured.Unstructured, 0, len(refs))
	for i, r := range refs {
		if r.APIVersion == "" || r.Kind == "" || r.Name == "" {
			return nil, invalid("objeto %d: apiVersion, kind e name são obrigatórios", i+1)
		}
		if r.Namespace != "" && !dns1123.MatchString(r.Namespace) {
			return nil, invalid("objeto %d: namespace %q inválido", i+1, r.Namespace)
		}
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(r.APIVersion)
		u.SetKind(r.Kind)
		u.SetNamespace(r.Namespace)
		u.SetName(r.Name)
		out = append(out, u)
	}
	return out, nil
}

// protectedNS are cluster plumbing: nothing in them (nor themselves) is
// deleted from the UI. kubectl stays available for the rare real need.
var protectedNS = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true}

// Delete removes the objects in reverse dependency order (Namespaces last),
// continuing past failures. Actions: deleted | absent | error. With dryRun
// the API server validates the deletion (admission included) without doing it.
func (a *Applier) Delete(ctx context.Context, objs []*unstructured.Unstructured, defaultNS string, dryRun bool) *Summary {
	if defaultNS == "" {
		defaultNS = "default"
	}
	ordered := order(objs)
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	sum := &Summary{DryRun: dryRun, Results: []Result{}}
	reset := false
	for _, u := range ordered {
		res := Result{APIVersion: u.GetAPIVersion(), Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace()}
		err := func() error {
			gvk := u.GroupVersionKind()
			mapping, err := a.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if meta.IsNoMatchError(err) && !reset {
				a.Mapper.Reset()
				reset = true
				mapping, err = a.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			}
			if meta.IsNoMatchError(err) {
				res.Action = "absent"
				res.Note = "tipo não existe no cluster"
				return nil
			}
			if err != nil {
				return err
			}
			var ri dynamic.ResourceInterface
			if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
				if u.GetNamespace() == "" {
					u.SetNamespace(defaultNS)
				}
				res.Namespace = u.GetNamespace()
				if protectedNS[res.Namespace] {
					return fmt.Errorf("o namespace %s é do sistema e não pode ser alterado por aqui", res.Namespace)
				}
				ri = a.Dynamic.Resource(mapping.Resource).Namespace(res.Namespace)
			} else {
				res.Namespace = ""
				if mapping.Resource.Group == "" && mapping.Resource.Resource == "namespaces" && (protectedNS[u.GetName()] || u.GetName() == "default") {
					return fmt.Errorf("o namespace %s é do sistema e não pode ser excluído", u.GetName())
				}
				ri = a.Dynamic.Resource(mapping.Resource)
			}
			if _, err := ri.Get(ctx, u.GetName(), metav1.GetOptions{}); apierrors.IsNotFound(err) {
				res.Action = "absent"
				return nil
			} else if err != nil {
				return err
			}
			bg := metav1.DeletePropagationBackground
			opts := metav1.DeleteOptions{PropagationPolicy: &bg}
			if dryRun {
				opts.DryRun = []string{metav1.DryRunAll}
			}
			if err := ri.Delete(ctx, u.GetName(), opts); apierrors.IsNotFound(err) {
				res.Action = "absent"
				return nil
			} else if err != nil {
				return err
			}
			res.Action = "deleted"
			if mapping.Resource.Group == "" && mapping.Resource.Resource == "namespaces" {
				res.Note = "tudo o que estiver dentro do namespace também é excluído"
			}
			return nil
		}()
		if err != nil {
			res.Action, res.Error = "error", cleanError(err)
			sum.Failed++
		}
		sum.Results = append(sum.Results, res)
	}
	return sum
}
