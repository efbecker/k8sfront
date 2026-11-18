package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/deploy"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/registry"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// registryView never carries the password, only HasPassword.
type registryView struct {
	store.Registry
	HasPassword bool   `json:"hasPassword"`
	SecretName  string `json:"secretName"`
}

func viewRegistry(r *store.Registry) registryView {
	return registryView{Registry: *r, HasPassword: r.PasswordSecretID != nil, SecretName: registry.SecretName(r)}
}

type registryInput struct {
	Name          *string   `json:"name"`
	Kind          *string   `json:"kind"`
	Host          *string   `json:"host"`
	Endpoint      *string   `json:"endpoint"`
	Mirrors       *[]string `json:"mirrors"`
	Username      *string   `json:"username"`
	Password      *string   `json:"password"`
	ClearPassword bool      `json:"clearPassword"`
	CACert        *string   `json:"caCert"`
	SkipVerify    *bool     `json:"skipVerify"`
	PullSecret    *bool     `json:"pullSecret"`
	Namespaces    *[]string `json:"namespaces"`
	ClusterIDs    *[]string `json:"clusterIds"`
}

func (in *registryInput) apply(r *store.Registry) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&r.Name, in.Name)
	set(&r.Kind, in.Kind)
	set(&r.Host, in.Host)
	set(&r.Endpoint, in.Endpoint)
	set(&r.Username, in.Username)
	set(&r.CACert, in.CACert)
	if in.Mirrors != nil {
		r.Mirrors = *in.Mirrors
	}
	if in.SkipVerify != nil {
		r.SkipVerify = *in.SkipVerify
	}
	if in.PullSecret != nil {
		r.PullSecret = *in.PullSecret
	}
	if in.Namespaces != nil {
		r.Namespaces = *in.Namespaces
	}
	if in.ClusterIDs != nil {
		r.ClusterIDs = *in.ClusterIDs
	}
}

func (s *Server) registryValidation(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, "validation", strings.TrimPrefix(err.Error(), registry.ErrInvalid.Error()+": "))
}

// validateRegistry normalizes the registry and checks its cluster list.
func (s *Server) validateRegistry(ctx context.Context, w http.ResponseWriter, r *store.Registry, password string) bool {
	if err := registry.Normalize(r); err != nil {
		s.registryValidation(w, err)
		return false
	}
	if password != "" && r.Username == "" {
		writeError(w, http.StatusBadRequest, "validation", "informe o usuário junto com a senha/token")
		return false
	}
	if strings.ContainsAny(password, "\r\n") || len(password) > 4096 {
		writeError(w, http.StatusBadRequest, "validation", "senha/token inválido")
		return false
	}
	ids := []string{}
	for _, id := range r.ClusterIDs {
		if slices.Contains(ids, id) {
			continue
		}
		if _, err := s.Store.GetCluster(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusBadRequest, "validation", "cluster "+id+" não existe")
			} else {
				s.storeError(w, err)
			}
			return false
		}
		ids = append(ids, id)
	}
	r.ClusterIDs = ids
	return true
}

func registryAudit(r *store.Registry, passwordChanged bool) map[string]any {
	return map[string]any{"name": r.Name, "kind": r.Kind, "host": r.Host, "endpoint": r.Endpoint, "mirrors": r.Mirrors,
		"username": r.Username, "passwordChanged": passwordChanged, "caCert": r.CACert != "", "skipVerify": r.SkipVerify,
		"pullSecret": r.PullSecret, "namespaces": r.Namespaces, "clusterIds": r.ClusterIDs}
}

func (s *Server) listRegistries(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListRegistries(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := make([]registryView, 0, len(list))
	for i := range list {
		out = append(out, viewRegistry(&list[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRegistry(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.GetRegistry(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewRegistry(g))
}

func (s *Server) createRegistry(w http.ResponseWriter, r *http.Request) {
	var in registryInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	g := &store.Registry{ID: uuid.NewString(), PullSecret: true}
	in.apply(g)
	password := ""
	if in.Password != nil {
		password = *in.Password
	}
	if !s.validateRegistry(r.Context(), w, g, password) {
		return
	}
	if password != "" {
		id, err := s.Secrets.Put(r.Context(), store.SecretRegistryAuth, []byte(password))
		if err != nil {
			s.storeError(w, err)
			return
		}
		g.PasswordSecretID = &id
	}
	if err := s.Store.CreateRegistry(r.Context(), g); err != nil {
		if g.PasswordSecretID != nil {
			_ = s.Secrets.Delete(context.Background(), *g.PasswordSecretID)
		}
		s.storeError(w, err)
		return
	}
	s.audit(r, "registry.create", "registry", g.ID, registryAudit(g, password != ""))
	writeJSON(w, http.StatusCreated, viewRegistry(g))
}

func (s *Server) updateRegistry(w http.ResponseWriter, r *http.Request) {
	var in registryInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	g, err := s.Store.GetRegistry(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	in.apply(g)
	password := ""
	if in.Password != nil {
		password = *in.Password
	}
	if !s.validateRegistry(r.Context(), w, g, password) {
		return
	}
	old := g.PasswordSecretID
	changed := false
	switch {
	case password != "":
		id, err := s.Secrets.Put(r.Context(), store.SecretRegistryAuth, []byte(password))
		if err != nil {
			s.storeError(w, err)
			return
		}
		g.PasswordSecretID, changed = &id, true
	case in.ClearPassword || g.Username == "":
		g.PasswordSecretID, changed = nil, old != nil
	}
	if err := s.Store.UpdateRegistry(r.Context(), g); err != nil {
		if password != "" {
			_ = s.Secrets.Delete(context.Background(), *g.PasswordSecretID)
		}
		s.storeError(w, err)
		return
	}
	if changed && old != nil {
		_ = s.Secrets.Delete(r.Context(), *old)
	}
	s.audit(r, "registry.update", "registry", g.ID, registryAudit(g, changed))
	writeJSON(w, http.StatusOK, viewRegistry(g))
}

func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.GetRegistry(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.Store.DeleteRegistry(r.Context(), g.ID); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "registry.delete", "registry", g.ID, map[string]any{"name": g.Name, "host": g.Host})
	w.WriteHeader(http.StatusNoContent)
}

// checkRegistry tests reachability and the stored credentials (GET /v2/).
func (s *Server) checkRegistry(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.GetRegistry(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	pw, err := s.Secrets.GetOptional(r.Context(), g.PasswordSecretID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	res := registry.Check(r.Context(), g, string(pw))
	for i := range pw {
		pw[i] = 0
	}
	s.audit(r, "registry.check", "registry", g.ID, map[string]any{"ok": res.OK, "authenticated": res.Authenticated})
	writeJSON(w, http.StatusOK, res)
}

// syncRegistries applies the registries to a cluster (containerd on every
// node + imagePullSecrets).
func (s *Server) syncRegistries(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	s.submitMaintenance(w, r, c, ops.KindRegistriesSync, ops.ClusterParams{ClusterID: c.ID}, nil)
}

// catalogImage adds the registry known to the platform.
type catalogImage struct {
	kube.CatalogImage
	Registry string `json:"registry,omitempty"`
}

type catalogView struct {
	*kube.Catalog
	Images []catalogImage `json:"images"`
}

func (s *Server) imageCatalog(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	regs, err := s.Store.ListRegistries(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	cat, err := kube.ListImages(ctx, cl.Client)
	if err != nil {
		s.kubeError(w, err)
		return
	}
	out := catalogView{Catalog: cat, Images: make([]catalogImage, 0, len(cat.Images))}
	for _, im := range cat.Images {
		ci := catalogImage{CatalogImage: im}
		for i := range regs {
			if regs[i].Host == im.Host && regs[i].AppliesTo(cl.ID) {
				ci.Registry = regs[i].Name
			}
		}
		out.Images = append(out.Images, ci)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) prepullImages(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Images []string `json:"images"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	var images []string
	for _, im := range in.Images {
		im = strings.TrimSpace(im)
		if im == "" || slices.Contains(images, im) {
			continue
		}
		if _, err := registry.ParseImage(im); err != nil {
			s.registryValidation(w, err)
			return
		}
		images = append(images, im)
	}
	if len(images) == 0 || len(images) > 20 {
		writeError(w, http.StatusBadRequest, "validation", "informe de 1 a 20 imagens")
		return
	}
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	s.submitMaintenance(w, r, c, ops.KindImagePrepull, ops.PrepullParams{ClusterID: c.ID, Images: images}, map[string]any{"images": images})
}

func (s *Server) pruneImages(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerIDs []string `json:"serverIds"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
			return
		}
	}
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	members, err := s.Store.ListClusterServers(r.Context(), c.ID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	for _, id := range in.ServerIDs {
		if !slices.ContainsFunc(members, func(m store.Server) bool { return m.ID == id }) {
			writeError(w, http.StatusBadRequest, "validation", "servidor "+id+" não pertence ao cluster")
			return
		}
	}
	s.submitMaintenance(w, r, c, ops.KindImagePrune, ops.PruneParams{ClusterID: c.ID, ServerIDs: in.ServerIDs}, map[string]any{"serverIds": in.ServerIDs})
}

// --- deploy center integration ---------------------------------------------------

// resolveRegistryPullSecret fills a form pull secret that references a
// registered registry (withPassword only for the apply, never the preview).
// When the form has no pull secret and the image comes from a registry with
// credentials, that registry is selected automatically.
func (s *Server) resolveRegistryPullSecret(ctx context.Context, w http.ResponseWriter, app *deploy.AppSpec, clusterID string, withPassword bool) bool {
	regs, err := s.Store.ListRegistries(ctx)
	if err != nil {
		s.storeError(w, err)
		return false
	}
	if app.PullSecret == nil {
		if m := registry.Match(app.Image, regs); m != nil && m.PasswordSecretID != nil && m.PullSecret && (clusterID == "" || m.AppliesTo(clusterID)) {
			app.PullSecret = &deploy.PullSecret{RegistryID: m.ID}
		}
	}
	p := app.PullSecret
	if p == nil || p.RegistryID == "" {
		return true
	}
	i := slices.IndexFunc(regs, func(g store.Registry) bool { return g.ID == p.RegistryID })
	if i < 0 {
		writeError(w, http.StatusBadRequest, "validation", "registry selecionado não existe mais")
		return false
	}
	g := &regs[i]
	if g.Username == "" || g.PasswordSecretID == nil {
		writeError(w, http.StatusBadRequest, "validation", "o registry "+g.Name+" não tem credenciais cadastradas")
		return false
	}
	p.Name, p.Server, p.Username, p.Password = registry.SecretName(g), registry.AuthKey(g), g.Username, ""
	if withPassword {
		pw, err := s.Secrets.Get(ctx, *g.PasswordSecretID)
		if err != nil {
			s.storeError(w, err)
			return false
		}
		p.Password = string(pw)
		for j := range pw {
			pw[j] = 0
		}
	}
	return true
}

// ensurePullSecrets reconciles the registry pull secrets of the namespaces a
// deploy touches (best effort: a failure is logged, the deploy result stands).
// Namespaces that do not exist yet are skipped (after=false) or waited for.
func (s *Server) ensurePullSecrets(ctx context.Context, clusterID string, cl *kube.Cluster, namespaces []string, after bool) {
	if s.Secrets == nil || cl.Client == nil {
		return
	}
	specs, err := kube.DesiredPullSecrets(ctx, s.Store, s.Secrets, clusterID)
	if err != nil || len(specs) == 0 {
		if err != nil {
			s.Log.Warn("pull secrets", "cluster", clusterID, "err", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, ns := range namespaces {
		if ns == "" || registry.IsSystemNamespace(ns) {
			continue
		}
		if !after {
			if ok, err := kube.NamespaceExists(ctx, cl.Client, ns); err == nil && !ok {
				continue
			}
		}
		changes, err := kube.SyncPullSecrets(ctx, cl.Client, cl.Metadata, specs, ns)
		for _, c := range changes {
			if c.Error != "" {
				s.Log.Warn("pull secret", "cluster", clusterID, "namespace", ns, "secret", c.Name, "err", c.Error)
			}
		}
		if err != nil {
			s.Log.Warn("pull secrets", "cluster", clusterID, "namespace", ns, "err", err)
		}
	}
}

// touchedNamespaces lists the namespaces of a batch (plus the default one).
func touchedNamespaces(objs []*unstructured.Unstructured, def string) []string {
	if def == "" {
		def = "default"
	}
	out := []string{def}
	for _, u := range objs {
		ns := u.GetNamespace()
		if u.GetKind() == "Namespace" && u.GroupVersionKind().Group == "" {
			ns = u.GetName()
		}
		if ns != "" && !slices.Contains(out, ns) {
			out = append(out, ns)
		}
	}
	return out
}
