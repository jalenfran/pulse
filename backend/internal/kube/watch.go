package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/jalenfran/pulse/backend/internal/store"
)

// ── Failed Jobs ────────────────────────────────────────────────────────────

// jobFailedEvent turns a Job that has failed into an event. ok is false for a
// Job that hasn't failed. The event is keyed per CronJob, so the cooldown
// applies to "the nightly backup", not to each run.
func jobFailedEvent(j *batchv1.Job) (extID string, e store.Event, ok bool) {
	var failed *batchv1.JobCondition
	for i := range j.Status.Conditions {
		c := &j.Status.Conditions[i]
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			failed = c
		}
	}
	if failed == nil {
		return "", store.Event{}, false
	}
	what, name := "Job", j.Name
	for _, o := range j.OwnerReferences {
		if o.Kind == "CronJob" {
			what, name = "CronJob", o.Name
		}
	}
	ts := failed.LastTransitionTime.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	body := failed.Reason
	if failed.Message != "" {
		body += ": " + failed.Message
	}
	if j.Status.Failed > 0 {
		body += fmt.Sprintf(" (%d failed pod", j.Status.Failed)
		if j.Status.Failed > 1 {
			body += "s"
		}
		body += ")"
	}
	return "job-failed:" + string(j.UID), store.Event{
		TS:       ts,
		Source:   "k8s",
		Kind:     "job-failed",
		Severity: "warning",
		Title:    fmt.Sprintf("%s %s/%s failed", what, j.Namespace, name),
		Body:     body,
		Key:      fmt.Sprintf("job-failed:%s/%s", j.Namespace, name),
	}, true
}

// onJob records a failed Job. It handles adds as well as updates, so a
// failure that happened while pulse was restarting still reaches the
// timeline. The event carries the Job's own failure time, so seeing the same
// failure again changes nothing, and the notifier ignores old failures.
func (c *Cluster) onJob(obj any) {
	j, ok := obj.(*batchv1.Job)
	if !ok {
		return
	}
	extID, e, failed := jobFailedEvent(j)
	if !failed {
		return
	}
	// Name the most recent pod, so its logs are one step away in the app.
	if pods, err := c.pods.Pods(j.Namespace).List(labels.SelectorFromSet(labels.Set{"job-name": j.Name})); err == nil && len(pods) > 0 {
		sort.Slice(pods, func(a, b int) bool { return pods[a].CreationTimestamp.After(pods[b].CreationTimestamp.Time) })
		e.Body += "\nLast pod: " + pods[0].Name
	}
	c.upsert(extID, e)
}

// ── Unhealthy workloads ────────────────────────────────────────────────────

// workloadGrace is how long a workload may run short of ready replicas before
// it counts as down. A rollout, or a pod rescheduled after a node reboot,
// legitimately takes a few minutes.
const workloadGrace = 10 * time.Minute

type workloadState struct {
	ref          string // "Kind/namespace/name"
	w            Workload
	crashlooping bool   // already reported as pod-crashloop
	reasons      string // why its pods aren't ready
}

// workloadWatch tracks how long each workload has been unhealthy. It only
// lives in memory: after a restart the grace period starts over, and the
// notification cooldown (which is persisted) stops a repeat alert.
type workloadWatch struct {
	grace   time.Duration
	since   map[string]time.Time
	alerted map[string]bool
}

func newWorkloadWatch(grace time.Duration) *workloadWatch {
	return &workloadWatch{grace: grace, since: map[string]time.Time{}, alerted: map[string]bool{}}
}

// step takes the current state of every replicated workload and returns the
// events to record: a workload that has been short of ready replicas for the
// whole grace period, and one that recovered after being reported.
func (ww *workloadWatch) step(now time.Time, states []workloadState) []store.Event {
	var out []store.Event
	seen := make(map[string]bool, len(states))
	for _, s := range states {
		seen[s.ref] = true
		w := s.w
		if w.Healthy {
			if ww.alerted[s.ref] {
				out = append(out, store.Event{TS: now, Source: "k8s", Kind: "workload-recovered", Severity: "info",
					Title: fmt.Sprintf("%s %s/%s is healthy again (%s)", w.Kind, w.Namespace, w.Name, w.Status),
					Body:  "Was down for " + minutes(now.Sub(ww.since[s.ref])),
					Key:   "workload-recovered:" + s.ref})
			}
			delete(ww.since, s.ref)
			delete(ww.alerted, s.ref)
			continue
		}
		first, tracked := ww.since[s.ref]
		if !tracked {
			ww.since[s.ref] = now
			continue
		}
		// A crashloop has its own alert; don't say it twice. If the crashloop
		// gives way to some other failure, this still fires.
		if ww.alerted[s.ref] || s.crashlooping || now.Sub(first) < ww.grace {
			continue
		}
		ww.alerted[s.ref] = true
		out = append(out, store.Event{TS: now, Source: "k8s", Kind: "workload-unhealthy", Severity: "warning",
			Title: fmt.Sprintf("%s %s/%s has %s ready", w.Kind, w.Namespace, w.Name, w.Status),
			Body:  fmt.Sprintf("For %s. %s", minutes(now.Sub(first)), s.reasons),
			Key:   "workload-unhealthy:" + s.ref})
	}
	for ref := range ww.since { // deleted workloads
		if !seen[ref] {
			delete(ww.since, ref)
			delete(ww.alerted, ref)
		}
	}
	return out
}

// watchWorkloads checks workload health once a minute until ctx ends.
func (c *Cluster) watchWorkloads(ctx context.Context) {
	ww := newWorkloadWatch(workloadGrace)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		states, err := c.workloadStates()
		if err != nil {
			c.log.Error("workload watch", "err", err)
		} else {
			for _, e := range ww.step(time.Now(), states) {
				c.record(e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (c *Cluster) workloadStates() ([]workloadState, error) {
	workloads, err := c.Workloads("")
	if err != nil {
		return nil, err
	}
	pods, err := c.pods.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	byOwner := map[string][]*corev1.Pod{} // "Kind/ns/name"
	for _, p := range pods {
		if ref := ownerRef(p); ref != "" {
			kind, name, _ := strings.Cut(ref, "/")
			key := kind + "/" + p.Namespace + "/" + name
			byOwner[key] = append(byOwner[key], p)
		}
	}
	states := make([]workloadState, 0, len(workloads))
	for _, w := range workloads {
		if w.Kind == "CronJob" { // reported per run, as job-failed
			continue
		}
		ref := w.Kind + "/" + w.Namespace + "/" + w.Name
		s := workloadState{ref: ref, w: w}
		if !w.Healthy {
			s.crashlooping, s.reasons = podReasons(byOwner[ref])
		}
		states = append(states, s)
	}
	return states, nil
}

// podReasons explains why a workload's pods aren't ready, in a few lines.
func podReasons(pods []*corev1.Pod) (crashlooping bool, reasons string) {
	if len(pods) == 0 {
		return false, "No pods exist: check the controller's events (quota, admission, a bad template)."
	}
	var lines []string
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if waitingFor(cs, "CrashLoopBackOff") {
				crashlooping = true
			}
		}
		why := ""
		if w, bad := podProblem(p); bad {
			why = w
		} else if p.Status.Phase == corev1.PodPending {
			why = "Pending"
			for _, cond := range p.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Message != "" {
					why = "can't be scheduled: " + cond.Message
				}
			}
		} else if p.Status.Phase == corev1.PodRunning {
			ready := 0
			for _, cs := range p.Status.ContainerStatuses {
				if cs.Ready {
					ready++
				}
			}
			if ready < len(p.Spec.Containers) {
				why = fmt.Sprintf("running but not ready (%d/%d containers): check its readiness probe", ready, len(p.Spec.Containers))
			}
		}
		if why != "" {
			lines = append(lines, p.Name+": "+why)
		}
	}
	sort.Strings(lines)
	if len(lines) > 3 {
		lines = append(lines[:3], fmt.Sprintf("…and %d more", len(lines)-3))
	}
	if len(lines) == 0 {
		return crashlooping, "Its pods look fine individually; it may still be rolling out."
	}
	return crashlooping, strings.Join(lines, "\n")
}
