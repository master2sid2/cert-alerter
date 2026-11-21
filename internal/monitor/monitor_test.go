package monitor_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/monitor"
	"github.com/master2sid2/cert-alerter/internal/secret"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/testutil"
)

func TestFullCheckAndSchedulerLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := testutil.SQLite(t)
	st := store.New(d)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	box, _ := secret.NewBox(make([]byte, 32))
	set := settings.NewService(st, box, "", log)
	eng := alerts.New(st, set, &testutil.FakeSender{}, log)
	mon := monitor.New(ctx, st, set, eng, log, "instance-a", true)
	ca := testutil.NewCA(t)
	mon.Roots = ca.Pool

	good := ca.Serve(t, time.Now().Add(40*24*time.Hour))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	for _, port := range []int{good, closed} {
		c := &store.Certificate{URL: "x", Host: "127.0.0.1", Port: port, Enabled: true}
		if err := st.CreateCertificate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	if !mon.StartFullCheck() {
		t.Fatal("full check did not start")
	}
	deadline := time.Now().Add(30 * time.Second)
	for mon.Status(ctx).CheckRunning || mon.Status(ctx).LastFullCheck == nil {
		if time.Now().After(deadline) {
			t.Fatal("full check did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	certs, _ := st.ListCertificates(ctx)
	var ok, failed int
	for _, c := range certs {
		switch {
		case c.LastCheckedAt == nil:
			t.Fatalf("certificate %d not checked", c.ID)
		case c.LastError != "":
			failed++
		case c.NotAfter != nil && c.Trusted:
			ok++
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("ok=%d failed=%d", ok, failed)
	}
	if v, found, _ := st.GetSetting(ctx, "state.last_full_check"); !found || v == "" {
		t.Fatal("last full check time not persisted")
	}

	// The scheduler loop takes the lease and releases it on shutdown.
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { mon.Run(runCtx); close(done) }()
	deadline = time.Now().Add(10 * time.Second)
	for !mon.Status(ctx).Leader {
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not become leader")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ok, _ := st.TryLock(ctx, "scheduler", "instance-b", time.Minute); ok {
		t.Fatal("second instance obtained the lease while the first holds it")
	}
	stop()
	<-done
	if ok, _ := st.TryLock(ctx, "scheduler", "instance-b", time.Minute); !ok {
		t.Fatal("lease was not released on shutdown")
	}
	mon.Wait()
}
