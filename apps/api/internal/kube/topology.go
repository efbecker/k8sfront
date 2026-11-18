package kube

import (
	"context"
	"log/slog"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// Topology event types sent to subscribers.
const (
	TopoSnapshot = "snapshot"
	TopoUpsert   = "upsert"
	TopoDelete   = "delete"
	TopoStatus   = "status"
)

// Connection states reported in TopoStatus events.
const (
	StateConnecting = "connecting"
	StateLive       = "live"
	StateError      = "error"
)

// TopoEvent is one message of the live topology stream.
type TopoEvent struct {
	Type     string    `json:"type"`
	Kind     string    `json:"kind,omitempty"` // node | pod | service
	UID      string    `json:"uid,omitempty"`
	Node     *Node     `json:"node,omitempty"`
	Pod      *Pod      `json:"pod,omitempty"`
	Service  *Service  `json:"service,omitempty"`
	Nodes    []Node    `json:"nodes,omitempty"`
	Pods     []Pod     `json:"pods,omitempty"`
	Services []Service `json:"services,omitempty"`
	State    string    `json:"state,omitempty"`
	Message  string    `json:"message,omitempty"`
	At       time.Time `json:"at"`
}

// TopoSub receives events; C is closed when the subscriber falls behind or the
// watcher stops, and the client should reconnect for a fresh snapshot.
type TopoSub struct {
	C      chan TopoEvent
	closed bool
}

// Tunables (overridden in tests).
var (
	topoIdleTimeout   = 2 * time.Minute
	topoHealthEvery   = 15 * time.Second
	topoSyncTimeout   = 30 * time.Second
	topoSubBufferSize = 2048
)

// Topology keeps a live, slim copy of nodes, pods and services using shared
// informers. Informers run only while someone is watching (plus a grace
// period), so idle clusters cost nothing.
type Topology struct {
	cs  kubernetes.Interface
	log *slog.Logger

	mu       sync.Mutex
	running  bool
	synced   bool
	state    string
	message  string
	cancel   context.CancelFunc
	idle     *time.Timer
	subs     map[*TopoSub]struct{}
	nodes    map[string]Node
	pods     map[string]Pod
	services map[string]Service
	// slices: EndpointSlice "ns/name" → owning service "ns/name" and its ready IPs.
	slices map[string]sliceInfo
}

type sliceInfo struct {
	service string
	ips     []string
}

func NewTopology(cs kubernetes.Interface, log *slog.Logger) *Topology {
	return &Topology{cs: cs, log: log, subs: map[*TopoSub]struct{}{}}
}

// Subscribe registers a subscriber and returns the events it must receive
// first (current status and, when the cache is ready, a full snapshot).
// Everything after that arrives on sub.C, with no gap in between.
func (t *Topology) Subscribe() (*TopoSub, []TopoEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.idle != nil {
		t.idle.Stop()
		t.idle = nil
	}
	if !t.running {
		t.startLocked()
	}
	sub := &TopoSub{C: make(chan TopoEvent, topoSubBufferSize)}
	t.subs[sub] = struct{}{}
	initial := []TopoEvent{t.statusLocked()}
	if t.synced {
		initial = append(initial, t.snapshotLocked())
	}
	return sub, initial
}

// Unsubscribe removes the subscriber; the informers stop after an idle period.
func (t *Topology) Unsubscribe(sub *TopoSub) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dropLocked(sub)
	if len(t.subs) == 0 && t.running && t.idle == nil {
		t.idle = time.AfterFunc(topoIdleTimeout, func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if len(t.subs) == 0 {
				t.stopLocked()
			}
		})
	}
}

// Stop halts the informers and disconnects every subscriber.
func (t *Topology) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for s := range t.subs {
		t.dropLocked(s)
	}
	t.stopLocked()
}

func (t *Topology) dropLocked(sub *TopoSub) {
	if _, ok := t.subs[sub]; !ok {
		return
	}
	delete(t.subs, sub)
	if !sub.closed {
		sub.closed = true
		close(sub.C)
	}
}

func (t *Topology) stopLocked() {
	if t.idle != nil {
		t.idle.Stop()
		t.idle = nil
	}
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.running, t.synced = false, false
	t.nodes, t.pods, t.services, t.slices = nil, nil, nil, nil
}

func (t *Topology) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.running, t.synced = cancel, true, false
	t.state, t.message = StateConnecting, ""
	t.nodes, t.pods, t.services = map[string]Node{}, map[string]Pod{}, map[string]Service{}
	t.slices = map[string]sliceInfo{}

	factory := informers.NewSharedInformerFactoryWithOptions(t.cs, 0, informers.WithTransform(stripManagedFields))
	nodeInf := factory.Core().V1().Nodes().Informer()
	podInf := factory.Core().V1().Pods().Informer()
	svcInf := factory.Core().V1().Services().Informer()
	sliceInf := factory.Discovery().V1().EndpointSlices().Informer()
	for _, inf := range []cache.SharedIndexInformer{nodeInf, podInf, svcInf, sliceInf} {
		_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
			{
				t.setStatus(ctx, StateError, "falha ao observar a API do Kubernetes: "+err.Error())
			}
		})
	}
	_, _ = nodeInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { t.onNode(ctx, o, false) },
		UpdateFunc: func(_, o any) { t.onNode(ctx, o, false) },
		DeleteFunc: func(o any) { t.onNode(ctx, o, true) },
	})
	_, _ = podInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { t.onPod(ctx, o, false) },
		UpdateFunc: func(_, o any) { t.onPod(ctx, o, false) },
		DeleteFunc: func(o any) { t.onPod(ctx, o, true) },
	})
	_, _ = svcInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { t.onService(ctx, o, false) },
		UpdateFunc: func(_, o any) { t.onService(ctx, o, false) },
		DeleteFunc: func(o any) { t.onService(ctx, o, true) },
	})
	_, _ = sliceInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { t.onSlice(ctx, o, false) },
		UpdateFunc: func(_, o any) { t.onSlice(ctx, o, false) },
		DeleteFunc: func(o any) { t.onSlice(ctx, o, true) },
	})
	factory.Start(ctx.Done())

	go func() {
		sctx, scancel := context.WithTimeout(ctx, topoSyncTimeout)
		defer scancel()
		if !cache.WaitForCacheSync(sctx.Done(), nodeInf.HasSynced, podInf.HasSynced, svcInf.HasSynced, sliceInf.HasSynced) {
			if ctx.Err() == nil {
				t.setStatus(ctx, StateError, "a API do Kubernetes não respondeu a tempo; tentando novamente")
				// Keep waiting: informers retry on their own.
				if !cache.WaitForCacheSync(ctx.Done(), nodeInf.HasSynced, podInf.HasSynced, svcInf.HasSynced, sliceInf.HasSynced) {
					return
				}
			} else {
				return
			}
		}
		t.mu.Lock()
		if ctx.Err() != nil {
			t.mu.Unlock()
			return
		}
		t.synced = true
		t.state, t.message = StateLive, ""
		t.broadcastLocked(t.statusLocked())
		t.broadcastLocked(t.snapshotLocked())
		t.mu.Unlock()
		t.healthLoop(ctx)
	}()
}

// healthLoop flips the status between live and error based on a cheap
// discovery call, since informers do not report recovery.
func (t *Topology) healthLoop(ctx context.Context) {
	tick := time.NewTicker(topoHealthEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			rc := t.cs.Discovery().RESTClient()
			if rc == nil {
				continue
			}
			hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := rc.Get().AbsPath("/readyz").DoRaw(hctx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				t.setStatus(ctx, StateError, ReadyzMessage(err))
			} else {
				t.setStatus(ctx, StateLive, "")
			}
		}
	}
}

func (t *Topology) setStatus(ctx context.Context, state, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil || (t.state == state && t.message == msg) {
		return
	}
	if state == StateLive && !t.synced {
		return
	}
	t.state, t.message = state, msg
	t.broadcastLocked(t.statusLocked())
}

func (t *Topology) statusLocked() TopoEvent {
	return TopoEvent{Type: TopoStatus, State: t.state, Message: t.message, At: time.Now().UTC()}
}

func (t *Topology) snapshotLocked() TopoEvent {
	ev := TopoEvent{Type: TopoSnapshot, Nodes: make([]Node, 0, len(t.nodes)), Pods: make([]Pod, 0, len(t.pods)),
		Services: make([]Service, 0, len(t.services)), At: time.Now().UTC()}
	for _, n := range t.nodes {
		ev.Nodes = append(ev.Nodes, n)
	}
	for _, p := range t.pods {
		ev.Pods = append(ev.Pods, p)
	}
	for _, s := range t.services {
		ev.Services = append(ev.Services, s)
	}
	sort.Slice(ev.Nodes, func(i, j int) bool { return ev.Nodes[i].Name < ev.Nodes[j].Name })
	sort.Slice(ev.Pods, func(i, j int) bool {
		return ev.Pods[i].Namespace+"/"+ev.Pods[i].Name < ev.Pods[j].Namespace+"/"+ev.Pods[j].Name
	})
	sort.Slice(ev.Services, func(i, j int) bool {
		return ev.Services[i].Namespace+"/"+ev.Services[i].Name < ev.Services[j].Namespace+"/"+ev.Services[j].Name
	})
	return ev
}

// broadcastLocked fans out without blocking: a subscriber whose buffer is
// full is disconnected and will resync from a fresh snapshot.
func (t *Topology) broadcastLocked(ev TopoEvent) {
	for s := range t.subs {
		select {
		case s.C <- ev:
		default:
			t.dropLocked(s)
		}
	}
}

func tombstone(o any) any {
	if d, ok := o.(cache.DeletedFinalStateUnknown); ok {
		return d.Obj
	}
	return o
}

func (t *Topology) onNode(ctx context.Context, o any, deleted bool) {
	n, ok := tombstone(o).(*corev1.Node)
	if !ok {
		return
	}
	v := nodeView(n)
	t.apply(ctx, "node", v.UID, deleted, func() bool {
		if deleted {
			_, had := t.nodes[v.UID]
			delete(t.nodes, v.UID)
			return had
		}
		if old, had := t.nodes[v.UID]; had && reflect.DeepEqual(old, v) {
			return false
		}
		t.nodes[v.UID] = v
		return true
	}, TopoEvent{Node: &v})
}

func (t *Topology) onPod(ctx context.Context, o any, deleted bool) {
	p, ok := tombstone(o).(*corev1.Pod)
	if !ok {
		return
	}
	v := podView(p)
	t.apply(ctx, "pod", v.UID, deleted, func() bool {
		if deleted {
			_, had := t.pods[v.UID]
			delete(t.pods, v.UID)
			return had
		}
		if old, had := t.pods[v.UID]; had && reflect.DeepEqual(old, v) {
			return false
		}
		t.pods[v.UID] = v
		return true
	}, TopoEvent{Pod: &v})
}

func (t *Topology) onService(ctx context.Context, o any, deleted bool) {
	s, ok := tombstone(o).(*corev1.Service)
	if !ok {
		return
	}
	v := serviceView(s)
	t.apply(ctx, "service", v.UID, deleted, func() bool {
		if deleted {
			_, had := t.services[v.UID]
			delete(t.services, v.UID)
			return had
		}
		if len(v.Selector) == 0 {
			v.EndpointIPs = t.endpointIPsLocked(v.Namespace + "/" + v.Name)
		}
		if old, had := t.services[v.UID]; had && reflect.DeepEqual(old, v) {
			return false
		}
		t.services[v.UID] = v
		return true
	}, TopoEvent{Service: &v})
}

// onSlice tracks EndpointSlices; when the IPs behind a selectorless service
// change, that service is re-sent with its new EndpointIPs.
func (t *Topology) onSlice(ctx context.Context, o any, deleted bool) {
	es, ok := tombstone(o).(*discoveryv1.EndpointSlice)
	if !ok {
		return
	}
	svcName := es.Labels[discoveryv1.LabelServiceName]
	if svcName == "" {
		return
	}
	key := es.Namespace + "/" + es.Name
	info := sliceInfo{service: es.Namespace + "/" + svcName}
	for _, ep := range es.Endpoints {
		if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
			continue
		}
		info.ips = append(info.ips, ep.Addresses...)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil || !t.running {
		return
	}
	old, had := t.slices[key]
	if deleted {
		delete(t.slices, key)
		info = old
	} else {
		if had && reflect.DeepEqual(old, info) {
			return
		}
		t.slices[key] = info
	}
	for uid, sv := range t.services {
		if len(sv.Selector) != 0 || sv.Namespace+"/"+sv.Name != info.service {
			continue
		}
		ips := t.endpointIPsLocked(info.service)
		if reflect.DeepEqual(ips, sv.EndpointIPs) {
			return
		}
		sv.EndpointIPs = ips
		t.services[uid] = sv
		if t.synced {
			t.broadcastLocked(TopoEvent{Type: TopoUpsert, Kind: "service", UID: uid, Service: &sv, At: time.Now().UTC()})
		}
		return
	}
}

// endpointIPsLocked returns the sorted, de-duplicated ready IPs of a service.
func (t *Topology) endpointIPsLocked(service string) []string {
	seen := map[string]bool{}
	var out []string
	for _, si := range t.slices {
		if si.service != service {
			continue
		}
		for _, ip := range si.ips {
			if !seen[ip] {
				seen[ip] = true
				out = append(out, ip)
			}
		}
	}
	sort.Strings(out)
	return out
}

// apply mutates the cache under the lock and, once the initial sync is done,
// broadcasts the change.
func (t *Topology) apply(ctx context.Context, kind, uid string, deleted bool, mutate func() bool, ev TopoEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// ctx is cancelled under t.mu, so events from a stopped generation never land.
	if ctx.Err() != nil || !t.running {
		return
	}
	if !mutate() || !t.synced {
		return
	}
	ev.Kind, ev.UID, ev.At = kind, uid, time.Now().UTC()
	if deleted {
		ev = TopoEvent{Type: TopoDelete, Kind: kind, UID: uid, At: ev.At}
	} else {
		ev.Type = TopoUpsert
	}
	t.broadcastLocked(ev)
}

func stripManagedFields(o any) (any, error) {
	if a, err := meta.Accessor(o); err == nil {
		a.SetManagedFields(nil)
		a.SetAnnotations(nil)
	}
	return o, nil
}

var failedCheck = regexp.MustCompile(`\[-\]([a-zA-Z0-9_./-]+) failed`)

// ReadyzMessage turns a /readyz failure (a wall of "[+]check ok" lines) into
// one short sentence naming only the failing checks.
func ReadyzMessage(err error) string {
	msg := err.Error()
	var failed []string
	for _, m := range failedCheck.FindAllStringSubmatch(msg, -1) {
		if !slices.Contains(failed, m[1]) {
			failed = append(failed, m[1])
		}
	}
	if len(failed) > 0 {
		return "API do Kubernetes respondendo com falhas (" + strings.Join(failed, ", ") + "); comum por alguns segundos durante upgrade ou reinício do control plane"
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return "API do Kubernetes inacessível: " + msg
}
