package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

// MaxObjects bounds one apply.
const MaxObjects = 200

// Parse reads multi-document YAML (or JSON), expanding `kind: List`.
func Parse(manifest string) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(manifest)), 4096)
	var out []*unstructured.Unstructured
	for doc := 1; ; doc++ {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, invalid("documento %d: YAML inválido: %v", doc, err)
		}
		if len(m) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: m}
		if u.IsList() {
			list, err := u.ToList()
			if err != nil {
				return nil, invalid("documento %d: lista inválida: %v", doc, err)
			}
			for i := range list.Items {
				out = append(out, &list.Items[i])
			}
			continue
		}
		out = append(out, u)
	}
	if len(out) == 0 {
		return nil, invalid("nenhum objeto no manifesto")
	}
	if len(out) > MaxObjects {
		return nil, invalid("no máximo %d objetos por apply (recebidos %d)", MaxObjects, len(out))
	}
	for i, u := range out {
		if u.GetAPIVersion() == "" || u.GetKind() == "" {
			return nil, invalid("objeto %d: apiVersion e kind são obrigatórios", i+1)
		}
		if u.GetName() == "" {
			return nil, invalid("objeto %d (%s): metadata.name é obrigatório (generateName não é suportado no apply)", i+1, u.GetKind())
		}
	}
	return out, nil
}

// Result is the outcome for one object. Object contents are never echoed
// (they may be Secrets).
type Result struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	// Action: created | configured | unchanged | error.
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

// Summary is the response of an apply.
type Summary struct {
	DryRun  bool     `json:"dryRun"`
	Results []Result `json:"results"`
	Failed  int      `json:"failed"`
}

// order applies Namespaces and CRDs first so later objects can use them.
func order(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
	rank := func(u *unstructured.Unstructured) int {
		switch u.GetKind() {
		case "Namespace":
			return 0
		case "CustomResourceDefinition":
			return 1
		case "ServiceAccount", "ClusterRole", "Role", "ConfigMap", "Secret", "PersistentVolumeClaim", "StorageClass":
			return 2
		case "ClusterRoleBinding", "RoleBinding":
			return 3
		}
		return 4
	}
	out := append([]*unstructured.Unstructured(nil), objs...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// Applier performs server-side apply.
type Applier struct {
	Dynamic dynamic.Interface
	Mapper  meta.ResettableRESTMapper
}

// Apply server-side-applies every object (continuing past failures, like
// kubectl). With dryRun the API server validates and runs admission without
// persisting anything (dryRun=All).
func (a *Applier) Apply(ctx context.Context, objs []*unstructured.Unstructured, defaultNS string, dryRun bool) *Summary {
	if defaultNS == "" {
		defaultNS = "default"
	}
	sum := &Summary{DryRun: dryRun, Results: []Result{}}
	reset := false
	for _, u := range order(objs) {
		res := Result{APIVersion: u.GetAPIVersion(), Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace()}
		err := func() error {
			gvk := u.GroupVersionKind()
			mapping, err := a.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if meta.IsNoMatchError(err) && !reset {
				// A CRD may have just been applied: refresh discovery once.
				a.Mapper.Reset()
				reset = true
				mapping, err = a.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			}
			if err != nil {
				if meta.IsNoMatchError(err) && dryRun {
					return fmt.Errorf("tipo %s desconhecido pelo cluster (se o CRD está neste mesmo manifesto, o dry-run não o cria)", gvk.String())
				}
				return fmt.Errorf("tipo %s desconhecido pelo cluster: %w", gvk.String(), err)
			}
			var ri dynamic.ResourceInterface
			if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
				if u.GetNamespace() == "" {
					u.SetNamespace(defaultNS)
				}
				res.Namespace = u.GetNamespace()
				ri = a.Dynamic.Resource(mapping.Resource).Namespace(u.GetNamespace())
			} else {
				u.SetNamespace("")
				res.Namespace = ""
				ri = a.Dynamic.Resource(mapping.Resource)
			}
			u.SetManagedFields(nil)
			u.SetResourceVersion("")
			prev, err := ri.Get(ctx, u.GetName(), metav1.GetOptions{})
			exists := err == nil
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			body, err := json.Marshal(u.Object)
			if err != nil {
				return err
			}
			force := true
			opts := metav1.PatchOptions{FieldManager: FieldManager, Force: &force}
			if dryRun {
				opts.DryRun = []string{metav1.DryRunAll}
			}
			out, err := ri.Patch(ctx, u.GetName(), types.ApplyPatchType, body, opts)
			if err != nil {
				return err
			}
			switch {
			case !exists:
				res.Action = "created"
			case out.GetResourceVersion() == prev.GetResourceVersion():
				res.Action = "unchanged"
			default:
				res.Action = "configured"
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

func cleanError(err error) string {
	var se apierrors.APIStatus
	if errors.As(err, &se) {
		return strings.TrimSpace(se.Status().Message)
	}
	return err.Error()
}

// Describe lists "Kind ns/name" for audit entries (no contents).
func Describe(objs []*unstructured.Unstructured) []string {
	out := make([]string, 0, len(objs))
	for _, u := range objs {
		id := u.GetName()
		if u.GetNamespace() != "" {
			id = u.GetNamespace() + "/" + id
		}
		out = append(out, u.GetKind()+" "+id)
	}
	return out
}
