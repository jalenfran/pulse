package kube

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestJobFailedEvent(t *testing.T) {
	at := time.Date(2026, 10, 16, 6, 31, 0, 0, time.UTC)
	j := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "db-dump-29866350", Namespace: "aperture", UID: "u1",
			OwnerReferences: []metav1.OwnerReference{{Kind: "CronJob", Name: "db-dump"}}},
		Status: batchv1.JobStatus{Failed: 3},
	}
	if _, _, ok := jobFailedEvent(j); ok {
		t.Fatal("a job without a Failed condition is not a failure")
	}

	j.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue},
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded",
			Message: "Job has reached the specified backoff limit", LastTransitionTime: metav1.NewTime(at)},
	}
	extID, e, ok := jobFailedEvent(j)
	if !ok {
		t.Fatal("expected a failure")
	}
	if extID != "job-failed:u1" || !e.TS.Equal(at) {
		t.Errorf("extID %q ts %v", extID, e.TS)
	}
	if e.Title != "CronJob aperture/db-dump failed" || e.Key != "job-failed:aperture/db-dump" {
		t.Errorf("title %q key %q: should name the CronJob, not the run", e.Title, e.Key)
	}
	if e.Body != "BackoffLimitExceeded: Job has reached the specified backoff limit (3 failed pods)" {
		t.Errorf("body %q", e.Body)
	}
}

func TestWorkloadWatch(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	ww := newWorkloadWatch(10 * time.Minute)
	down := func(crashlooping bool) []workloadState {
		return []workloadState{{ref: "Deployment/media/sonarr", crashlooping: crashlooping,
			reasons: "sonarr-abc: ImagePullBackOff",
			w:       Workload{Kind: "Deployment", Namespace: "media", Name: "sonarr", Status: "0/1"}}}
	}
	up := []workloadState{{ref: "Deployment/media/sonarr",
		w: Workload{Kind: "Deployment", Namespace: "media", Name: "sonarr", Status: "1/1", Healthy: true}}}

	if ev := ww.step(t0, down(false)); len(ev) != 0 {
		t.Fatalf("first sighting alerted: %+v", ev)
	}
	if ev := ww.step(t0.Add(9*time.Minute), down(false)); len(ev) != 0 {
		t.Fatalf("alerted inside the grace period: %+v", ev)
	}
	ev := ww.step(t0.Add(10*time.Minute), down(false))
	if len(ev) != 1 || ev[0].Kind != "workload-unhealthy" || ev[0].Severity != "warning" {
		t.Fatalf("after the grace period: %+v", ev)
	}
	if ev[0].Title != "Deployment media/sonarr has 0/1 ready" || !strings.Contains(ev[0].Body, "ImagePullBackOff") {
		t.Errorf("event = %+v", ev[0])
	}
	if ev := ww.step(t0.Add(11*time.Minute), down(false)); len(ev) != 0 {
		t.Errorf("alerted twice for one outage: %+v", ev)
	}

	ev = ww.step(t0.Add(25*time.Minute), up)
	if len(ev) != 1 || ev[0].Kind != "workload-recovered" || !strings.Contains(ev[0].Body, "25m") {
		t.Fatalf("recovery: %+v", ev)
	}

	// A blip shorter than the grace period says nothing, coming or going.
	ww.step(t0.Add(30*time.Minute), down(false))
	if ev := ww.step(t0.Add(33*time.Minute), up); len(ev) != 0 {
		t.Errorf("short blip produced %+v", ev)
	}

	// A crashloop has its own alert, so this one stays quiet while it lasts...
	ww.step(t0.Add(40*time.Minute), down(true))
	if ev := ww.step(t0.Add(55*time.Minute), down(true)); len(ev) != 0 {
		t.Errorf("duplicated the crashloop alert: %+v", ev)
	}
	// ...but speaks up if the outage outlives the crashloop.
	if ev := ww.step(t0.Add(56*time.Minute), down(false)); len(ev) != 1 {
		t.Errorf("outage that outlived the crashloop: %+v", ev)
	}

	// Deleting the workload forgets it.
	ww.step(t0.Add(60*time.Minute), nil)
	if len(ww.since) != 0 || len(ww.alerted) != 0 {
		t.Errorf("deleted workload still tracked: %v %v", ww.since, ww.alerted)
	}
}

func TestMinutes(t *testing.T) {
	for d, want := range map[time.Duration]string{
		11 * time.Minute:                "11m",
		11*time.Minute + 40*time.Second: "12m",
		65 * time.Minute:                "1h5m",
		2 * time.Hour:                   "2h",
		20 * time.Second:                "0m",
	} {
		if got := minutes(d); got != want {
			t.Errorf("minutes(%v) = %q, want %q", d, got, want)
		}
	}
}
