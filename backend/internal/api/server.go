// Package api is pulse's HTTP surface. api/openapi.yaml at the repo root is
// the contract; keep the two in step.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/jalenfran/pulse/backend/internal/kube"
	"github.com/jalenfran/pulse/backend/internal/store"
)

type Server struct {
	store   *store.Store
	cluster *kube.Cluster
	log     *slog.Logger
}

func New(st *store.Store, cluster *kube.Cluster, log *slog.Logger) *Server {
	return &Server{store: st, cluster: cluster, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.cluster.Synced() {
			http.Error(w, "syncing", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok\n")
	})

	user := s.requireToken("user")
	mux.Handle("GET /v1/overview", user(s.overview))
	mux.Handle("GET /v1/nodes", user(s.listNodes))
	mux.Handle("GET /v1/nodes/{name}", user(s.getNode))
	mux.Handle("GET /v1/workloads", user(s.listWorkloads))
	mux.Handle("GET /v1/pods", user(s.listPods))
	mux.Handle("GET /v1/pods/{namespace}/{pod}", user(s.getPod))
	mux.Handle("GET /v1/pods/{namespace}/{pod}/logs", user(s.podLogs))
	mux.Handle("GET /v1/pods/{namespace}/{pod}/logs/stream", user(s.streamPodLogs))
	mux.Handle("GET /v1/events", user(s.listEvents))
	mux.Handle("POST /v1/ingest", s.requireToken("host")(s.ingest))

	return s.logRequests(mux)
}

// ── Middleware ─────────────────────────────────────────────────────────────

type ctxKey struct{}

func tokenFrom(ctx context.Context) *store.Token {
	t, _ := ctx.Value(ctxKey{}).(*store.Token)
	return t
}

// logSlot lets requireToken tell logRequests who called. Auth runs further in
// and attaches the token to a derived request, which the logger never sees.
type logSlot struct{ token *store.Token }

type logSlotKey struct{}

// requireToken admits only bearer tokens of the given kind. User and host
// tokens are deliberately not interchangeable: a host token leaked from a
// node can post events but can't read the cluster.
func (s *Server) requireToken(kind string) func(http.HandlerFunc) http.Handler {
	return func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				writeError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			tok, err := s.store.Authenticate(r.Context(), raw)
			if err != nil {
				s.log.Error("authenticate", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if tok == nil || tok.Kind != kind {
				writeError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			if slot, ok := r.Context().Value(logSlotKey{}).(*logSlot); ok {
				slot.token = tok
			}
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, tok)))
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer for Flush.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		slot := &logSlot{}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), logSlotKey{}, slot)))
		attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond), "client", r.Header.Get("X-Real-IP")}
		if t := slot.token; t != nil {
			attrs = append(attrs, "token", t.Kind+"/"+t.Name)
		}
		s.log.Info("request", attrs...)
	})
}

// ── Responses ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

type list[T any] struct {
	Items []T `json:"items"`
}

// ── Handlers ───────────────────────────────────────────────────────────────

type hostStatus struct {
	Host        string       `json:"host"`
	LastUpgrade *store.Event `json:"lastUpgrade,omitempty"`
	LastHealth  *store.Event `json:"lastHealth,omitempty"`
	LastBoot    *store.Event `json:"lastBoot,omitempty"`
}

type overview struct {
	GeneratedAt time.Time          `json:"generatedAt"`
	Nodes       []kube.NodeSummary `json:"nodes"`
	Workloads   struct {
		Total   int `json:"total"`
		Healthy int `json:"healthy"`
	} `json:"workloads"`
	Pods struct {
		Total    int `json:"total"`
		Running  int `json:"running"`
		Problems int `json:"problems"`
	} `json:"pods"`
	Problems     []kube.Problem `json:"problems"`
	Hosts        []hostStatus   `json:"hosts"`
	RecentEvents []store.Event  `json:"recentEvents"`
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var o overview
	o.GeneratedAt = time.Now().UTC()

	var err error
	if o.Nodes, err = s.cluster.Nodes(ctx); err != nil {
		s.internalError(w, "nodes", err)
		return
	}
	workloads, err := s.cluster.Workloads("")
	if err != nil {
		s.internalError(w, "workloads", err)
		return
	}
	o.Workloads.Total = len(workloads)
	for _, wl := range workloads {
		if wl.Healthy {
			o.Workloads.Healthy++
		}
	}
	pods, err := s.cluster.Pods("", "")
	if err != nil {
		s.internalError(w, "pods", err)
		return
	}
	o.Pods.Total = len(pods)
	for _, p := range pods {
		if p.Phase == "Running" {
			o.Pods.Running++
		}
		if p.Problem {
			o.Pods.Problems++
		}
	}
	if o.Problems, err = s.cluster.Problems(); err != nil {
		s.internalError(w, "problems", err)
		return
	}
	if o.Problems == nil {
		o.Problems = []kube.Problem{}
	}

	// Every k8s node is a host, plus anything that has reported in without
	// being one (a machine outside the cluster with a host token).
	hosts := map[string]*hostStatus{}
	var order []string
	host := func(name string) *hostStatus {
		if h, ok := hosts[name]; ok {
			return h
		}
		hosts[name] = &hostStatus{Host: name}
		order = append(order, name)
		return hosts[name]
	}
	for _, n := range o.Nodes {
		host(n.Name)
	}
	latest, err := s.store.LatestHostEvents(ctx, []string{"nixos-upgrade", "node-health", "boot"})
	if err != nil {
		s.internalError(w, "host events", err)
		return
	}
	for i := range latest {
		e := &latest[i]
		h := host(strings.TrimPrefix(e.Source, "host:"))
		switch e.Kind {
		case "nixos-upgrade":
			h.LastUpgrade = e
		case "node-health":
			h.LastHealth = e
		case "boot":
			h.LastBoot = e
		}
	}
	o.Hosts = make([]hostStatus, 0, len(order))
	for _, name := range order {
		o.Hosts = append(o.Hosts, *hosts[name])
	}

	if o.RecentEvents, err = s.store.ListEvents(ctx, store.EventQuery{Limit: 10}); err != nil {
		s.internalError(w, "events", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.cluster.Nodes(r.Context())
	if err != nil {
		s.internalError(w, "nodes", err)
		return
	}
	writeJSON(w, http.StatusOK, list[kube.NodeSummary]{nodes})
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	node, err := s.cluster.Node(r.Context(), name)
	if errors.Is(err, kube.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such node")
		return
	}
	if err != nil {
		s.internalError(w, "node", err)
		return
	}
	events, err := s.store.ListEvents(r.Context(), store.EventQuery{Source: "host:" + name, Limit: 20})
	if err != nil {
		s.internalError(w, "events", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*kube.NodeDetail
		Events []store.Event `json:"events"`
	}{node, events})
}

func (s *Server) listWorkloads(w http.ResponseWriter, r *http.Request) {
	items, err := s.cluster.Workloads(r.URL.Query().Get("namespace"))
	if err != nil {
		s.internalError(w, "workloads", err)
		return
	}
	if items == nil {
		items = []kube.Workload{}
	}
	writeJSON(w, http.StatusOK, list[kube.Workload]{items})
}

func (s *Server) listPods(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.cluster.Pods(q.Get("namespace"), q.Get("node"))
	if err != nil {
		s.internalError(w, "pods", err)
		return
	}
	writeJSON(w, http.StatusOK, list[kube.PodSummary]{items})
}

func (s *Server) getPod(w http.ResponseWriter, r *http.Request) {
	pod, err := s.cluster.Pod(r.PathValue("namespace"), r.PathValue("pod"))
	if errors.Is(err, kube.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such pod")
		return
	}
	if err != nil {
		s.internalError(w, "pod", err)
		return
	}
	writeJSON(w, http.StatusOK, pod)
}

// dnsName matches Kubernetes object names. Path values go to the API server
// as path segments, so this is belt and braces rather than the only check.
var dnsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

func logRequest(r *http.Request) (ns, pod string, o kube.LogOptions, err error) {
	ns, pod = r.PathValue("namespace"), r.PathValue("pod")
	if !dnsName.MatchString(ns) || !dnsName.MatchString(pod) {
		return "", "", o, errors.New("invalid namespace or pod name")
	}
	q := r.URL.Query()
	o.Container = q.Get("container")
	if o.Container != "" && !dnsName.MatchString(o.Container) {
		return "", "", o, errors.New("invalid container name")
	}
	o.Tail = 200
	if v := q.Get("tail"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > 5000 {
			return "", "", o, errors.New("tail must be 1-5000")
		}
		o.Tail = n
	}
	o.Timestamps = q.Get("timestamps") == "true"
	o.Previous = q.Get("previous") == "true"
	return ns, pod, o, nil
}

// logError maps an API server error on a log request to a response. The API
// server's messages ("container x is not valid for pod y", "previous
// terminated container not found") are useful to the app, so they pass
// through.
func (s *Server) logError(w http.ResponseWriter, err error) {
	var status apierrors.APIStatus
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "no such pod")
	case errors.As(err, &status) && status.Status().Code == http.StatusBadRequest:
		writeError(w, http.StatusBadRequest, status.Status().Message)
	default:
		s.internalError(w, "pod logs", err)
	}
}

func (s *Server) podLogs(w http.ResponseWriter, r *http.Request) {
	ns, pod, opts, err := logRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rc, err := s.cluster.Logs(r.Context(), ns, pod, opts)
	if err != nil {
		s.logError(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.Copy(w, rc)
}

func (s *Server) streamPodLogs(w http.ResponseWriter, r *http.Request) {
	ns, pod, opts, err := logRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts.Follow, opts.Previous = true, false
	ctx := r.Context()
	rc, err := s.cluster.Logs(ctx, ns, pod, opts)
	if err != nil {
		s.logError(w, err)
		return
	}
	// Closing the stream is what unblocks the scanner goroutine when the
	// client goes away first.
	defer rc.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // tells nginx not to buffer the stream
	w.WriteHeader(http.StatusOK)
	flusher := http.NewResponseController(w)
	_ = flusher.Flush()

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- strings.ReplaceAll(sc.Text(), "\r", ""):
			case <-ctx.Done():
				return
			}
		}
	}()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				fmt.Fprint(w, "event: end\ndata: \n\n")
				_ = flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			// Write whatever else is already buffered before flushing, so a
			// burst of lines (the initial tail) goes out as one write. Only
			// this goroutine receives, so len() can't go stale under us.
			for len(lines) > 0 {
				fmt.Fprintf(w, "data: %s\n\n", <-lines)
			}
			if err := flusher.Flush(); err != nil {
				return
			}
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			if err := flusher.Flush(); err != nil {
				return
			}
		}
	}
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	eq := store.EventQuery{
		Before: q.Get("before"),
		Source: q.Get("source"),
		Kind:   q.Get("kind"),
		Limit:  50,
	}
	if v := q.Get("minSeverity"); v != "" {
		rank, ok := store.SeverityRank(v)
		if !ok {
			writeError(w, http.StatusBadRequest, "minSeverity must be info, warning or critical")
			return
		}
		eq.MinSeverity = rank
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "limit must be 1-200")
			return
		}
		eq.Limit = n
	}
	// Fetch one extra row to know whether there is another page.
	limit := eq.Limit
	eq.Limit++
	items, err := s.store.ListEvents(r.Context(), eq)
	if err != nil {
		s.internalError(w, "events", err)
		return
	}
	resp := struct {
		Items      []store.Event `json:"items"`
		NextCursor string        `json:"nextCursor,omitempty"`
	}{Items: items}
	if len(items) > limit {
		resp.Items = items[:limit]
		resp.NextCursor = items[limit-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

var eventKind = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	tok := tokenFrom(r.Context())
	var in struct {
		Kind     string     `json:"kind"`
		Severity string     `json:"severity"`
		Title    string     `json:"title"`
		Body     string     `json:"body"`
		Key      string     `json:"key"`
		TS       *time.Time `json:"ts"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	switch {
	case !eventKind.MatchString(in.Kind):
		writeError(w, http.StatusBadRequest, "kind must match "+eventKind.String())
		return
	case in.Title == "" || len(in.Title) > 200:
		writeError(w, http.StatusBadRequest, "title is required, at most 200 bytes")
		return
	case len(in.Body) > 8192:
		writeError(w, http.StatusBadRequest, "body is at most 8192 bytes")
		return
	case len(in.Key) > 200:
		writeError(w, http.StatusBadRequest, "key is at most 200 bytes")
		return
	}
	if _, ok := store.SeverityRank(in.Severity); !ok {
		writeError(w, http.StatusBadRequest, "severity must be info, warning or critical")
		return
	}
	now := time.Now()
	ts := now
	// A host clock can be off. Never let one put events in the future, where
	// they would sit at the top of the timeline.
	if in.TS != nil && in.TS.Before(now) {
		ts = *in.TS
	}
	key := in.Key
	if key == "" {
		key = in.Kind + ":" + tok.Name
	}
	e, err := s.store.AddEvent(r.Context(), store.Event{
		TS:       ts,
		Source:   "host:" + tok.Name, // from the token, never the body
		Kind:     in.Kind,
		Severity: in.Severity,
		Title:    in.Title,
		Body:     in.Body,
		Key:      key,
	})
	if err != nil {
		s.internalError(w, "ingest", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}
