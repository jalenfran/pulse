// Package kube watches the cluster through shared informers and turns what it
// sees into pulse's view of it. Reads are served from the informer caches, not
// the API server, so the app can poll /v1/overview as often as it likes.
package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/jalenfran/pulse/backend/internal/store"
)

type Cluster struct {
	cs    kubernetes.Interface
	store *store.Store
	log   *slog.Logger

	factory   informers.SharedInformerFactory
	evFactory informers.SharedInformerFactory

	nodes    corelisters.NodeLister
	pods     corelisters.PodLister
	deploys  appslisters.DeploymentLister
	sts      appslisters.StatefulSetLister
	ds       appslisters.DaemonSetLister
	jobs     batchlisters.JobLister
	cronJobs batchlisters.CronJobLister

	synced atomic.Bool

	metricsMu   sync.Mutex
	metricsAt   time.Time
	nodeMetrics map[string]nodeUsage
}

func New(cfg *rest.Config, st *store.Store, log *slog.Logger) (*Cluster, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	const resync = 0 // handlers are edge-triggered; periodic resyncs would only replay
	c := &Cluster{
		cs:    cs,
		store: st,
		log:   log,
		factory: informers.NewSharedInformerFactoryWithOptions(cs, resync,
			informers.WithTransform(slim)),
		// Warnings only. Normal events are most of the volume and none of the
		// signal.
		evFactory: informers.NewSharedInformerFactoryWithOptions(cs, resync,
			informers.WithTransform(slim),
			informers.WithTweakListOptions(func(o *metav1.ListOptions) {
				o.FieldSelector = fields.OneTermEqualSelector("type", corev1.EventTypeWarning).String()
			})),
	}
	c.nodes = c.factory.Core().V1().Nodes().Lister()
	c.pods = c.factory.Core().V1().Pods().Lister()
	c.deploys = c.factory.Apps().V1().Deployments().Lister()
	c.sts = c.factory.Apps().V1().StatefulSets().Lister()
	c.ds = c.factory.Apps().V1().DaemonSets().Lister()
	c.jobs = c.factory.Batch().V1().Jobs().Lister()
	c.cronJobs = c.factory.Batch().V1().CronJobs().Lister()
	return c, nil
}

// slim drops the parts of cached objects pulse never reads. managedFields and
// last-applied-configuration are often larger than the rest of the object.
func slim(obj any) (any, error) {
	if m, err := meta.Accessor(obj); err == nil {
		m.SetManagedFields(nil)
		if a := m.GetAnnotations(); a != nil {
			delete(a, corev1.LastAppliedConfigAnnotation)
		}
	}
	return obj, nil
}

// Start runs the informers and blocks until their caches have synced.
func (c *Cluster) Start(ctx context.Context) error {
	if _, err := c.factory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: c.onNodeUpdate,
	}); err != nil {
		return err
	}
	if _, err := c.factory.Core().V1().Pods().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: c.onPodUpdate,
	}); err != nil {
		return err
	}
	if _, err := c.evFactory.Core().V1().Events().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.onK8sEvent(obj) },
		UpdateFunc: func(_, obj any) { c.onK8sEvent(obj) },
	}); err != nil {
		return err
	}

	c.factory.Start(ctx.Done())
	c.evFactory.Start(ctx.Done())
	for typ, ok := range c.factory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("cache for %v did not sync", typ)
		}
	}
	for typ, ok := range c.evFactory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("cache for %v did not sync", typ)
		}
	}
	c.synced.Store(true)
	return nil
}

func (c *Cluster) Synced() bool { return c.synced.Load() }

// ── Event mirroring ────────────────────────────────────────────────────────

func (c *Cluster) record(e store.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.store.AddEvent(ctx, e); err != nil {
		c.log.Error("store event", "kind", e.Kind, "err", err)
	}
}

func (c *Cluster) upsert(extID string, e store.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.store.UpsertExternal(ctx, extID, e); err != nil {
		c.log.Error("store event", "kind", e.Kind, "err", err)
	}
}

// onNodeUpdate records Ready transitions. Only transitions: a node that was
// already NotReady when pulse started shows up in the overview's problems,
// not as a fresh event.
func (c *Cluster) onNodeUpdate(oldObj, newObj any) {
	oldNode, ok1 := oldObj.(*corev1.Node)
	newNode, ok2 := newObj.(*corev1.Node)
	if !ok1 || !ok2 {
		return
	}
	wasReady, isReady := nodeReady(oldNode), nodeReady(newNode)
	if wasReady == isReady {
		return
	}
	name := newNode.Name
	if isReady {
		c.record(store.Event{TS: time.Now(), Source: "k8s", Kind: "node-ready", Severity: "info",
			Title: name + " is Ready again", Key: "node-ready:" + name})
		return
	}
	e := store.Event{TS: time.Now(), Source: "k8s", Kind: "node-notready", Severity: "warning",
		Title: name + " is NotReady", Key: "node-notready:" + name}
	cond := nodeCondition(newNode, corev1.NodeReady)
	if cond == nil {
		c.record(e)
		return
	}
	e.Body = cond.Reason + ": " + cond.Message
	c.record(e)

	// A node going NotReady is routine: every upgrade reboot does it for a
	// minute or two. Only a node that stays down is critical, which is what
	// gets through quiet hours. Matching the transition time makes sure it's
	// still the same outage, not a later one with its own timer.
	since := cond.LastTransitionTime
	time.AfterFunc(nodeDownAfter, func() {
		n, err := c.nodes.Get(name)
		if err != nil || nodeReady(n) {
			return
		}
		if now := nodeCondition(n, corev1.NodeReady); now == nil || !now.LastTransitionTime.Equal(&since) {
			return
		}
		c.record(store.Event{TS: time.Now(), Source: "k8s", Kind: "node-down", Severity: "critical",
			Title: fmt.Sprintf("%s has been NotReady for %s", name, nodeDownAfter),
			Body:  e.Body, Key: "node-down:" + name})
	})
}

// nodeDownAfter is how long a node may stay NotReady before it counts as
// down. Upgrade reboots here take about two minutes.
const nodeDownAfter = 5 * time.Minute

// onPodUpdate records a container entering CrashLoopBackOff. Each pod
// container gets one timeline row that is refreshed on every new crash,
// rather than one row per crash.
func (c *Cluster) onPodUpdate(oldObj, newObj any) {
	oldPod, ok1 := oldObj.(*corev1.Pod)
	newPod, ok2 := newObj.(*corev1.Pod)
	if !ok1 || !ok2 {
		return
	}
	for _, cs := range newPod.Status.ContainerStatuses {
		if !waitingFor(cs, "CrashLoopBackOff") {
			continue
		}
		if prev := findStatus(oldPod.Status.ContainerStatuses, cs.Name); prev != nil &&
			waitingFor(*prev, "CrashLoopBackOff") && prev.RestartCount == cs.RestartCount {
			continue
		}
		body := fmt.Sprintf("%d restarts", cs.RestartCount)
		if t := cs.LastTerminationState.Terminated; t != nil {
			body = fmt.Sprintf("%d restarts, last exit %d (%s)", cs.RestartCount, t.ExitCode, t.Reason)
		}
		c.upsert(fmt.Sprintf("crashloop:%s:%s", newPod.UID, cs.Name), store.Event{
			TS:       time.Now(),
			Source:   "k8s",
			Kind:     "pod-crashloop",
			Severity: "warning",
			Title:    fmt.Sprintf("%s/%s (%s) is crashlooping", newPod.Namespace, newPod.Name, cs.Name),
			Body:     body,
			Key:      fmt.Sprintf("pod-crashloop:%s/%s", newPod.Namespace, ownerName(newPod)),
		})
	}
}

// onK8sEvent mirrors a Kubernetes Warning event. Kubernetes bumps count and
// lastTimestamp on repeats, which refreshes the same row.
func (c *Cluster) onK8sEvent(obj any) {
	ev, ok := obj.(*corev1.Event)
	if !ok || ev.Type != corev1.EventTypeWarning {
		return
	}
	ts := ev.LastTimestamp.Time
	if ts.IsZero() {
		ts = ev.EventTime.Time
	}
	if ts.IsZero() {
		ts = ev.CreationTimestamp.Time
	}
	obj2 := ev.InvolvedObject
	target := obj2.Name
	if obj2.Namespace != "" {
		target = obj2.Namespace + "/" + obj2.Name
	}
	body := ev.Message
	if ev.Count > 1 {
		body = fmt.Sprintf("%s (x%d)", body, ev.Count)
	}
	c.upsert("k8s:"+string(ev.UID), store.Event{
		TS:       ts,
		Source:   "k8s",
		Kind:     "k8s-event",
		Severity: "warning",
		Title:    fmt.Sprintf("%s: %s %s", ev.Reason, obj2.Kind, target),
		Body:     body,
		Key:      fmt.Sprintf("k8s-event:%s:%s", target, ev.Reason),
	})
}

// ── Logs ───────────────────────────────────────────────────────────────────

type LogOptions struct {
	Container  string
	Tail       int64
	Follow     bool
	Previous   bool
	Timestamps bool
}

const maxLogBytes = 10 << 20

func (c *Cluster) Logs(ctx context.Context, namespace, pod string, o LogOptions) (io.ReadCloser, error) {
	opts := &corev1.PodLogOptions{
		Container:  o.Container,
		TailLines:  &o.Tail,
		Follow:     o.Follow,
		Previous:   o.Previous,
		Timestamps: o.Timestamps,
	}
	if !o.Follow {
		limit := int64(maxLogBytes)
		opts.LimitBytes = &limit
	}
	return c.cs.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
}

// ── Metrics ────────────────────────────────────────────────────────────────

type nodeUsage struct {
	cpuMilli int64
	memBytes int64
}

// nodeUsage returns metrics-server's node usage, cached for 15s. A missing or
// broken metrics-server means no usage figures, not an error.
func (c *Cluster) nodeUsage(ctx context.Context) map[string]nodeUsage {
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	if time.Since(c.metricsAt) < 15*time.Second {
		return c.nodeMetrics
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := c.cs.Discovery().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").DoRaw(ctx)
	c.metricsAt = time.Now()
	if err != nil {
		c.log.Warn("node metrics unavailable", "err", err)
		c.nodeMetrics = nil
		return nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage map[string]resource.Quantity `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		c.log.Warn("decode node metrics", "err", err)
		c.nodeMetrics = nil
		return nil
	}
	m := make(map[string]nodeUsage, len(list.Items))
	for _, it := range list.Items {
		cpu, mem := it.Usage["cpu"], it.Usage["memory"]
		m[it.Metadata.Name] = nodeUsage{cpuMilli: cpu.MilliValue(), memBytes: mem.Value()}
	}
	c.nodeMetrics = m
	return m
}
