package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

var ErrNotFound = errors.New("not found")

// pendingGrace is how long a pod may sit in Pending before it counts as a
// problem. Image pulls on a fresh node legitimately take a while.
const pendingGrace = 5 * time.Minute

type Usage struct {
	Used     int64   `json:"used"`
	Capacity int64   `json:"capacity"`
	Percent  float64 `json:"percent"`
}

type Condition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}

type NodeSummary struct {
	Name           string    `json:"name"`
	Ready          bool      `json:"ready"`
	Schedulable    bool      `json:"schedulable"`
	Roles          []string  `json:"roles"`
	KubeletVersion string    `json:"kubeletVersion"`
	KernelVersion  string    `json:"kernelVersion"`
	OSImage        string    `json:"osImage"`
	InternalIP     string    `json:"internalIP"`
	Pressure       []string  `json:"pressure"`
	PodCount       int       `json:"podCount"`
	CPU            *Usage    `json:"cpu,omitempty"`
	Memory         *Usage    `json:"memory,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type NodeDetail struct {
	NodeSummary
	Conditions []Condition  `json:"conditions"`
	Pods       []PodSummary `json:"pods"`
}

type PodSummary struct {
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	Node            string    `json:"node"`
	Phase           string    `json:"phase"`
	Status          string    `json:"status"`
	ReadyContainers int       `json:"readyContainers"`
	TotalContainers int       `json:"totalContainers"`
	Restarts        int       `json:"restarts"`
	Owner           string    `json:"owner,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	Problem         bool      `json:"problem"`
}

type Termination struct {
	Reason     string     `json:"reason"`
	ExitCode   int32      `json:"exitCode"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type ContainerInfo struct {
	Name            string       `json:"name"`
	Image           string       `json:"image"`
	Ready           bool         `json:"ready"`
	Restarts        int          `json:"restarts"`
	State           string       `json:"state"`
	Reason          string       `json:"reason,omitempty"`
	Message         string       `json:"message,omitempty"`
	StartedAt       *time.Time   `json:"startedAt,omitempty"`
	LastTermination *Termination `json:"lastTermination,omitempty"`
}

type PodDetail struct {
	PodSummary
	IP             string          `json:"ip,omitempty"`
	QOSClass       string          `json:"qosClass,omitempty"`
	InitContainers []ContainerInfo `json:"initContainers"`
	Containers     []ContainerInfo `json:"containers"`
	Conditions     []Condition     `json:"conditions"`
}

type Workload struct {
	Kind             string     `json:"kind"`
	Namespace        string     `json:"namespace"`
	Name             string     `json:"name"`
	Desired          int        `json:"desired"`
	Ready            int        `json:"ready"`
	Healthy          bool       `json:"healthy"`
	Status           string     `json:"status"`
	Restarts         int        `json:"restarts"`
	Images           []string   `json:"images"`
	Schedule         string     `json:"schedule,omitempty"`
	LastScheduleTime *time.Time `json:"lastScheduleTime,omitempty"`
}

type Problem struct {
	Severity  string `json:"severity"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
}

// ── Nodes ──────────────────────────────────────────────────────────────────

func (c *Cluster) Nodes(ctx context.Context) ([]NodeSummary, error) {
	nodes, err := c.nodes.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	pods, err := c.pods.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	perNode := map[string]int{}
	for _, p := range pods {
		if p.Spec.NodeName != "" && !podFinished(p) {
			perNode[p.Spec.NodeName]++
		}
	}
	usage := c.nodeUsage(ctx)
	out := make([]NodeSummary, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, summarizeNode(n, perNode[n.Name], usage))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c *Cluster) Node(ctx context.Context, name string) (*NodeDetail, error) {
	n, err := c.nodes.Get(name)
	if err != nil {
		return nil, ErrNotFound
	}
	pods, err := c.Pods("", name)
	if err != nil {
		return nil, err
	}
	running := 0
	for _, p := range pods {
		if p.Phase != string(corev1.PodSucceeded) && p.Phase != string(corev1.PodFailed) {
			running++
		}
	}
	d := &NodeDetail{
		NodeSummary: summarizeNode(n, running, c.nodeUsage(ctx)),
		Conditions:  make([]Condition, 0, len(n.Status.Conditions)),
		Pods:        pods,
	}
	for _, cond := range n.Status.Conditions {
		d.Conditions = append(d.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message, cond.LastTransitionTime.Time))
	}
	return d, nil
}

func summarizeNode(n *corev1.Node, podCount int, usage map[string]nodeUsage) NodeSummary {
	s := NodeSummary{
		Name:           n.Name,
		Ready:          nodeReady(n),
		Schedulable:    !n.Spec.Unschedulable,
		Roles:          []string{},
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		KernelVersion:  n.Status.NodeInfo.KernelVersion,
		OSImage:        n.Status.NodeInfo.OSImage,
		Pressure:       []string{},
		PodCount:       podCount,
		CreatedAt:      n.CreationTimestamp.UTC(),
	}
	for l := range n.Labels {
		if role, ok := strings.CutPrefix(l, "node-role.kubernetes.io/"); ok && role != "" {
			s.Roles = append(s.Roles, role)
		}
	}
	sort.Strings(s.Roles)
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			s.InternalIP = a.Address
			break
		}
	}
	for _, cond := range n.Status.Conditions {
		if cond.Type != corev1.NodeReady && cond.Status == corev1.ConditionTrue {
			s.Pressure = append(s.Pressure, string(cond.Type))
		}
	}
	if u, ok := usage[n.Name]; ok {
		alloc := n.Status.Allocatable
		s.CPU = newUsage(u.cpuMilli, alloc.Cpu().MilliValue())
		s.Memory = newUsage(u.memBytes, alloc.Memory().Value())
	}
	return s
}

func newUsage(used, capacity int64) *Usage {
	u := &Usage{Used: used, Capacity: capacity}
	if capacity > 0 {
		u.Percent = float64(int(float64(used)/float64(capacity)*1000)) / 10
	}
	return u
}

func nodeReady(n *corev1.Node) bool {
	cond := nodeCondition(n, corev1.NodeReady)
	return cond != nil && cond.Status == corev1.ConditionTrue
}

func nodeCondition(n *corev1.Node, t corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range n.Status.Conditions {
		if n.Status.Conditions[i].Type == t {
			return &n.Status.Conditions[i]
		}
	}
	return nil
}

// ── Pods ───────────────────────────────────────────────────────────────────

// Pods lists pods, optionally filtered by namespace and/or node.
func (c *Cluster) Pods(namespace, node string) ([]PodSummary, error) {
	var pods []*corev1.Pod
	var err error
	if namespace != "" {
		pods, err = c.pods.Pods(namespace).List(labels.Everything())
	} else {
		pods, err = c.pods.List(labels.Everything())
	}
	if err != nil {
		return nil, err
	}
	out := make([]PodSummary, 0, len(pods))
	for _, p := range pods {
		if node != "" && p.Spec.NodeName != node {
			continue
		}
		out = append(out, summarizePod(p))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c *Cluster) Pod(namespace, name string) (*PodDetail, error) {
	p, err := c.pods.Pods(namespace).Get(name)
	if err != nil {
		return nil, ErrNotFound
	}
	d := &PodDetail{
		PodSummary:     summarizePod(p),
		IP:             p.Status.PodIP,
		QOSClass:       string(p.Status.QOSClass),
		InitContainers: containerInfos(p.Spec.InitContainers, p.Status.InitContainerStatuses),
		Containers:     containerInfos(p.Spec.Containers, p.Status.ContainerStatuses),
		Conditions:     make([]Condition, 0, len(p.Status.Conditions)),
	}
	for _, cond := range p.Status.Conditions {
		d.Conditions = append(d.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message, cond.LastTransitionTime.Time))
	}
	return d, nil
}

func summarizePod(p *corev1.Pod) PodSummary {
	s := PodSummary{
		Namespace:       p.Namespace,
		Name:            p.Name,
		Node:            p.Spec.NodeName,
		Phase:           string(p.Status.Phase),
		Status:          podStatus(p),
		TotalContainers: len(p.Spec.Containers),
		Owner:           ownerRef(p),
		CreatedAt:       p.CreationTimestamp.UTC(),
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			s.ReadyContainers++
		}
		s.Restarts += int(cs.RestartCount)
	}
	_, s.Problem = podProblem(p)
	return s
}

// podStatus approximates kubectl's STATUS column.
func podStatus(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		return "Terminating"
	}
	if p.Status.Reason != "" {
		return p.Status.Reason // e.g. Evicted
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
			return "Init:" + w.Reason
		}
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			return "Init:" + t.Reason
		}
	}
	status := string(p.Status.Phase)
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			return w.Reason
		}
		if t := cs.State.Terminated; t != nil && t.Reason != "" {
			status = t.Reason
		}
	}
	return status
}

var badWaiting = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
}

// podProblem reports why a pod needs attention, if it does. Pods owned by a
// Job are skipped: their failures surface once, through the Job.
func podProblem(p *corev1.Pod) (string, bool) {
	if ownerKind(p) == "Job" {
		return "", false
	}
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range list {
			if w := cs.State.Waiting; w != nil && badWaiting[w.Reason] {
				return fmt.Sprintf("%s: %s", cs.Name, w.Reason), true
			}
		}
	}
	switch p.Status.Phase {
	case corev1.PodFailed:
		reason := p.Status.Reason
		if reason == "" {
			reason = "Failed"
		}
		return reason, true
	case corev1.PodPending:
		if time.Since(p.CreationTimestamp.Time) > pendingGrace {
			return "Pending for " + time.Since(p.CreationTimestamp.Time).Round(time.Minute).String(), true
		}
	}
	return "", false
}

func podFinished(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

func containerInfos(specs []corev1.Container, statuses []corev1.ContainerStatus) []ContainerInfo {
	out := make([]ContainerInfo, 0, len(specs))
	for _, spec := range specs {
		ci := ContainerInfo{Name: spec.Name, Image: spec.Image, State: "unknown"}
		if cs := findStatus(statuses, spec.Name); cs != nil {
			ci.Ready = cs.Ready
			ci.Restarts = int(cs.RestartCount)
			switch {
			case cs.State.Running != nil:
				ci.State = "running"
				ci.StartedAt = timePtr(cs.State.Running.StartedAt.Time)
			case cs.State.Waiting != nil:
				ci.State = "waiting"
				ci.Reason, ci.Message = cs.State.Waiting.Reason, cs.State.Waiting.Message
			case cs.State.Terminated != nil:
				ci.State = "terminated"
				ci.Reason, ci.Message = cs.State.Terminated.Reason, cs.State.Terminated.Message
				ci.StartedAt = timePtr(cs.State.Terminated.StartedAt.Time)
			}
			if t := cs.LastTerminationState.Terminated; t != nil {
				ci.LastTermination = &Termination{Reason: t.Reason, ExitCode: t.ExitCode, FinishedAt: timePtr(t.FinishedAt.Time)}
			}
		}
		out = append(out, ci)
	}
	return out
}

func findStatus(statuses []corev1.ContainerStatus, name string) *corev1.ContainerStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}
	return nil
}

func waitingFor(cs corev1.ContainerStatus, reason string) bool {
	return cs.State.Waiting != nil && cs.State.Waiting.Reason == reason
}

func ownerKind(p *corev1.Pod) string {
	for _, o := range p.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			return o.Kind
		}
	}
	return ""
}

// ownerRef names the workload a pod belongs to, resolving a Deployment's
// ReplicaSet back to the Deployment.
func ownerRef(p *corev1.Pod) string {
	for _, o := range p.OwnerReferences {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			if hash := p.Labels["pod-template-hash"]; hash != "" {
				if d, ok := strings.CutSuffix(o.Name, "-"+hash); ok {
					return "Deployment/" + d
				}
			}
		}
		return o.Kind + "/" + o.Name
	}
	return ""
}

func ownerName(p *corev1.Pod) string {
	if ref := ownerRef(p); ref != "" {
		return ref
	}
	return p.Name
}

// ── Workloads ──────────────────────────────────────────────────────────────

func (c *Cluster) Workloads(namespace string) ([]Workload, error) {
	var out []Workload
	pods, err := c.pods.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	restarts := map[string]int{} // "Kind/ns/name" -> restarts over current pods
	for _, p := range pods {
		if ref := ownerRef(p); ref != "" {
			for _, cs := range p.Status.ContainerStatuses {
				restarts[p.Namespace+"/"+ref] += int(cs.RestartCount)
			}
		}
	}
	match := func(ns string) bool { return namespace == "" || ns == namespace }

	deploys, err := c.deploys.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, d := range deploys {
		if !match(d.Namespace) {
			continue
		}
		desired := 1
		if d.Spec.Replicas != nil {
			desired = int(*d.Spec.Replicas)
		}
		out = append(out, replicated("Deployment", d.Namespace, d.Name, desired, int(d.Status.ReadyReplicas),
			images(d.Spec.Template.Spec), restarts))
	}

	sets, err := c.sts.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, s := range sets {
		if !match(s.Namespace) {
			continue
		}
		desired := 1
		if s.Spec.Replicas != nil {
			desired = int(*s.Spec.Replicas)
		}
		out = append(out, replicated("StatefulSet", s.Namespace, s.Name, desired, int(s.Status.ReadyReplicas),
			images(s.Spec.Template.Spec), restarts))
	}

	daemons, err := c.ds.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, d := range daemons {
		if !match(d.Namespace) {
			continue
		}
		out = append(out, replicated("DaemonSet", d.Namespace, d.Name, int(d.Status.DesiredNumberScheduled),
			int(d.Status.NumberReady), images(d.Spec.Template.Spec), restarts))
	}

	cronJobs, err := c.cronJobs.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, cj := range cronJobs {
		if !match(cj.Namespace) {
			continue
		}
		w := Workload{
			Kind:      "CronJob",
			Namespace: cj.Namespace,
			Name:      cj.Name,
			Images:    images(cj.Spec.JobTemplate.Spec.Template.Spec),
			Schedule:  cj.Spec.Schedule,
			Healthy:   true,
		}
		if cj.Status.LastScheduleTime != nil {
			w.LastScheduleTime = timePtr(cj.Status.LastScheduleTime.Time)
		}
		status, failed, err := c.lastJobStatus(cj.Namespace, cj.Name)
		if err != nil {
			return nil, err
		}
		w.Status, w.Healthy = status, !failed
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			w.Status = "Suspended"
		}
		out = append(out, w)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

func replicated(kind, ns, name string, desired, ready int, imgs []string, restarts map[string]int) Workload {
	return Workload{
		Kind:      kind,
		Namespace: ns,
		Name:      name,
		Desired:   desired,
		Ready:     ready,
		Healthy:   ready >= desired,
		Status:    fmt.Sprintf("%d/%d", ready, desired),
		Restarts:  restarts[ns+"/"+kind+"/"+name],
		Images:    imgs,
	}
}

// lastJobStatus reports how the most recent Job created by a CronJob went.
// Only the latest run counts, so an old failure that a later run fixed is not
// a problem.
func (c *Cluster) lastJobStatus(namespace, cronJob string) (status string, failed bool, err error) {
	jobs, err := c.jobs.Jobs(namespace).List(labels.Everything())
	if err != nil {
		return "", false, err
	}
	var latest *time.Time
	status = "Never run"
	for _, j := range jobs {
		owned := false
		for _, o := range j.OwnerReferences {
			if o.Kind == "CronJob" && o.Name == cronJob {
				owned = true
			}
		}
		if !owned || (latest != nil && !j.CreationTimestamp.After(*latest)) {
			continue
		}
		t := j.CreationTimestamp.Time
		latest = &t
		status, failed = "Running", false
		for _, cond := range j.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}
			switch cond.Type {
			case "Complete":
				status = "Succeeded"
			case "Failed":
				status, failed = "Failed", true
			}
		}
	}
	return status, failed, nil
}

func images(spec corev1.PodSpec) []string {
	out := make([]string, 0, len(spec.Containers))
	for _, ct := range spec.Containers {
		out = append(out, ct.Image)
	}
	return out
}

// ── Problems ───────────────────────────────────────────────────────────────

// Problems lists everything currently wrong, most severe first.
func (c *Cluster) Problems() ([]Problem, error) {
	var out []Problem

	nodes, err := c.nodes.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if !nodeReady(n) {
			p := Problem{Severity: "critical", Kind: "node", Name: n.Name, Title: n.Name + " is NotReady"}
			if cond := nodeCondition(n, corev1.NodeReady); cond != nil {
				p.Detail = cond.Message
			}
			out = append(out, p)
		}
		for _, cond := range n.Status.Conditions {
			if cond.Type != corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				out = append(out, Problem{Severity: "warning", Kind: "node", Name: n.Name,
					Title: fmt.Sprintf("%s has %s", n.Name, cond.Type), Detail: cond.Message})
			}
		}
		if n.Spec.Unschedulable {
			out = append(out, Problem{Severity: "warning", Kind: "node", Name: n.Name, Title: n.Name + " is cordoned"})
		}
	}

	pods, err := c.pods.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, p := range pods {
		why, bad := podProblem(p)
		if !bad {
			continue
		}
		pr := Problem{Severity: "warning", Kind: "pod", Namespace: p.Namespace, Name: p.Name,
			Title: fmt.Sprintf("%s/%s: %s", p.Namespace, p.Name, why)}
		// A Failed pod with a controller has already been replaced. Kubernetes
		// keeps the corpse around indefinitely, so it's leftover cleanup, not an
		// outage. If the replacement is also broken, the workload says so.
		if p.Status.Phase == corev1.PodFailed && ownerKind(p) != "" {
			pr.Severity = "info"
			pr.Detail = "Replaced by its controller; safe to delete."
		}
		out = append(out, pr)
	}

	workloads, err := c.Workloads("")
	if err != nil {
		return nil, err
	}
	for _, w := range workloads {
		if w.Healthy {
			continue
		}
		kind, title := "workload", fmt.Sprintf("%s %s/%s has %s ready", w.Kind, w.Namespace, w.Name, w.Status)
		if w.Kind == "CronJob" {
			kind, title = "job", fmt.Sprintf("CronJob %s/%s: last run failed", w.Namespace, w.Name)
		}
		out = append(out, Problem{Severity: "warning", Kind: kind, Namespace: w.Namespace, Name: w.Name, Title: title})
	}

	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func condition(typ, status, reason, message string, t time.Time) Condition {
	return Condition{Type: typ, Status: status, Reason: reason, Message: message, LastTransitionTime: timePtr(t)}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
