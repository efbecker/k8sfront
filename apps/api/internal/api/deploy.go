package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/deploy"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// applyTimeout bounds a synchronous apply (each object is one API call).
const applyTimeout = 60 * time.Second

func writeValidation(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, "validation", strings.TrimPrefix(err.Error(), deploy.ErrInvalid.Error()+": "))
}

type renderedObject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type renderResponse struct {
	YAML    string           `json:"yaml"`
	Objects []renderedObject `json:"objects"`
}

func describeObjects(us []*unstructured.Unstructured) []renderedObject {
	out := make([]renderedObject, 0, len(us))
	for _, u := range us {
		out = append(out, renderedObject{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace()})
	}
	return out
}

// renderApp turns the visual form into manifests (preview, nothing applied).
func (s *Server) renderApp(w http.ResponseWriter, r *http.Request) {
	var app deploy.AppSpec
	if err := decodeJSON(r, &app); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if err := app.Normalize(); err != nil {
		writeValidation(w, err)
		return
	}
	us, text, err := app.Render()
	if err != nil {
		writeValidation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renderResponse{YAML: text, Objects: describeObjects(us)})
}

func (s *Server) applier(w http.ResponseWriter, r *http.Request) (*deploy.Applier, bool) {
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return nil, false
	}
	if cl.Dynamic == nil || cl.Mapper == nil {
		writeError(w, http.StatusConflict, "not_ready", "cliente dinâmico indisponível para este cluster")
		return nil, false
	}
	return &deploy.Applier{Dynamic: cl.Dynamic, Mapper: cl.Mapper}, true
}

func (s *Server) finishApply(w http.ResponseWriter, r *http.Request, action string, objs []*unstructured.Unstructured, sum *deploy.Summary) {
	if !sum.DryRun {
		s.audit(r, action, "cluster", r.PathValue("id"), map[string]any{"objects": deploy.Describe(objs), "failed": sum.Failed})
	}
	writeJSON(w, http.StatusOK, sum)
}

// deployApp renders the form and server-side-applies it (plus the registry
// credentials Secret, built only here).
func (s *Server) deployApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		App    deploy.AppSpec `json:"app"`
		DryRun bool           `json:"dryRun"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if err := in.App.Normalize(); err != nil {
		writeValidation(w, err)
		return
	}
	us, _, err := in.App.Render()
	if err != nil {
		writeValidation(w, err)
		return
	}
	if sec, err := in.App.PullSecretObject(); err != nil {
		writeValidation(w, err)
		return
	} else if sec != nil {
		us = append(us, sec)
	}
	a, ok := s.applier(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), applyTimeout)
	defer cancel()
	s.finishApply(w, r, "deploy.app", us, a.Apply(ctx, us, in.App.Namespace, in.DryRun))
}

// applyManifest server-side-applies YAML from the editor (dryRun=server first).
func (s *Server) applyManifest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Manifest  string `json:"manifest"`
		Namespace string `json:"namespace"`
		DryRun    bool   `json:"dryRun"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if in.Namespace != "" && !namespaceRe.MatchString(in.Namespace) {
		writeError(w, http.StatusBadRequest, "validation", "namespace inválido")
		return
	}
	objs, err := deploy.Parse(in.Manifest)
	if err != nil {
		writeValidation(w, err)
		return
	}
	a, ok := s.applier(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), applyTimeout)
	defer cancel()
	s.finishApply(w, r, "deploy.apply", objs, a.Apply(ctx, objs, in.Namespace, in.DryRun))
}

// deleteObjects removes the objects of a manifest (the Central de Deploy
// "Excluir") or an explicit list (a row on Workloads). dryRun validates first.
func (s *Server) deleteObjects(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Manifest  string             `json:"manifest"`
		Objects   []deploy.ObjectRef `json:"objects"`
		App       *deploy.AppSpec    `json:"app"`
		Namespace string             `json:"namespace"`
		// KeepNamespaces skips Namespace objects of the manifest.
		KeepNamespaces bool `json:"keepNamespaces"`
		// AlsoNamespaces adds whole namespaces (deleted last), offered by the
		// UI when the deletion leaves them empty.
		AlsoNamespaces []string `json:"alsoNamespaces"`
		DryRun         bool     `json:"dryRun"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if in.Namespace != "" && !namespaceRe.MatchString(in.Namespace) {
		writeError(w, http.StatusBadRequest, "validation", "namespace inválido")
		return
	}
	var objs []*unstructured.Unstructured
	var err error
	switch {
	case in.App != nil:
		// Everything the form generates for this app, pull secret included.
		if err = in.App.Normalize(); err == nil {
			objs, _, err = in.App.Render()
		}
		if ref := in.App.CreatedPullSecretRef(); err == nil && ref != nil {
			var sec []*unstructured.Unstructured
			if sec, err = deploy.FromRefs([]deploy.ObjectRef{*ref}); err == nil {
				objs = append(objs, sec...)
			}
		}
		in.Namespace = in.App.Namespace
	case strings.TrimSpace(in.Manifest) != "":
		objs, err = deploy.Parse(in.Manifest)
	default:
		objs, err = deploy.FromRefs(in.Objects)
	}
	if err != nil {
		writeValidation(w, err)
		return
	}
	if in.KeepNamespaces {
		kept := objs[:0]
		for _, u := range objs {
			if !(u.GetKind() == "Namespace" && u.GroupVersionKind().Group == "") {
				kept = append(kept, u)
			}
		}
		objs = kept
		if len(objs) == 0 {
			writeError(w, http.StatusBadRequest, "validation", "nenhum objeto para excluir além dos namespaces")
			return
		}
	}
	if len(in.AlsoNamespaces) > 0 {
		refs := make([]deploy.ObjectRef, 0, len(in.AlsoNamespaces))
		for _, n := range in.AlsoNamespaces {
			if !namespaceRe.MatchString(n) {
				writeError(w, http.StatusBadRequest, "validation", "namespace inválido")
				return
			}
			refs = append(refs, deploy.ObjectRef{APIVersion: "v1", Kind: "Namespace", Name: n})
		}
		extra, err := deploy.FromRefs(refs)
		if err == nil && len(objs)+len(extra) > deploy.MaxObjects {
			err = deploy.ErrInvalid
		}
		if err != nil {
			writeValidation(w, err)
			return
		}
		for _, e := range extra {
			if !containsNamespace(objs, e.GetName()) {
				objs = append(objs, e)
			}
		}
	}
	a, ok := s.applier(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), applyTimeout)
	defer cancel()
	s.finishApply(w, r, "deploy.delete", objs, a.Delete(ctx, objs, in.Namespace, in.DryRun))
}

func containsNamespace(objs []*unstructured.Unstructured, name string) bool {
	for _, u := range objs {
		if u.GetKind() == "Namespace" && u.GetName() == name {
			return true
		}
	}
	return false
}

func (s *Server) helmReleases(w http.ResponseWriter, r *http.Request) {
	ns, ok := namespaceParam(w, r)
	if !ok {
		return
	}
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	if cl.Metadata == nil {
		writeJSON(w, http.StatusOK, []deploy.HelmRelease{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	out, err := deploy.ListHelmReleases(ctx, cl.Metadata, ns)
	if err != nil {
		s.kubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// helmJob installs/upgrades or uninstalls a chart through the Helm CLI on the
// primary control plane. Values go encrypted to the vault, never into params.
func (s *Server) helmJob(w http.ResponseWriter, r *http.Request) {
	var in deploy.HelmInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if err := in.Normalize(); err != nil {
		writeValidation(w, err)
		return
	}
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	p := ops.HelmParams{ClusterID: c.ID, Action: in.Action, Release: in.Release, Namespace: in.Namespace, Chart: in.Chart, Repo: in.Repo, Version: in.Version}
	if strings.TrimSpace(in.Values) != "" {
		id, err := s.Secrets.Put(r.Context(), store.SecretHelmValues, []byte(in.Values))
		if err != nil {
			s.storeError(w, err)
			return
		}
		p.ValuesSecretID = id
	}
	job, err := s.Engine.Submit(r.Context(), ops.KindHelm, "cluster", c.ID, p, "admin")
	if err != nil {
		if p.ValuesSecretID != "" {
			_ = s.Secrets.Delete(context.Background(), p.ValuesSecretID)
		}
		s.storeError(w, err)
		return
	}
	s.audit(r, "deploy.helm_"+in.Action, "cluster", c.ID, map[string]any{"jobId": job.ID, "command": in.String(), "values": p.ValuesSecretID != ""})
	writeJSON(w, http.StatusAccepted, job)
}

// readyCluster loads a cluster that already has a kubeconfig.
func (s *Server) readyCluster(w http.ResponseWriter, r *http.Request) (*store.Cluster, bool) {
	c, err := s.Store.GetCluster(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return nil, false
	}
	if c.KubeconfigSecretID == nil {
		writeError(w, http.StatusConflict, "not_ready", kube.ErrNoKubeconfig.Error())
		return nil, false
	}
	return c, true
}

var errBusy = errors.New("já existe uma operação de manutenção em andamento neste cluster; aguarde ou cancele-a")
