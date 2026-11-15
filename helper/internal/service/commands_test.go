package service

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

func TestAddressedPortalOnly(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	hermes := h.create("hermes", localApp(8787))
	atlas := h.create("atlas", localApp(8788))
	if _, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: hermes, Destination: localApp(9000)}); err != nil {
		t.Fatal(err)
	}
	if got := h.portal(atlas); got.Destination != localApp(8788) || got.Name != "atlas" {
		t.Fatalf("unaddressed Portal changed: %+v", got)
	}
	_, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: newUUID(), Destination: localApp(9001)})
	wantCode(t, err, CodeNotFound)
}

func TestDestinationCommit(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	receipt, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: id, Destination: localApp(9000)})
	if err != nil || receipt.Outcome != OutcomeAccepted {
		t.Fatalf("SetDestination = %+v, %v", receipt, err)
	}
	h.settle()
	disk := h.disk().Installation.Portals[0]
	if disk.ID != id || disk.Name != "hermes" || disk.Destination != localApp(9000) {
		t.Fatalf("committed Portal = %+v", disk)
	}
	if last := h.factory.runtime().lastReconcile(); last[0].Destination != localApp(9000) {
		t.Fatalf("runtime destination = %+v", last[0].Destination)
	}

	// Setting the current value records a no-op receipt that same-ID recovery returns.
	req := h.req()
	receipt, err = h.session.SetDestination(req, SetDestinationParams{PortalID: id, Destination: localApp(9000)})
	if err != nil || receipt.Outcome != OutcomeNoOp {
		t.Fatalf("same-value SetDestination = %+v, %v", receipt, err)
	}
	result, err := h.session.GetRequestResult(req.ID, req.IssuedAt)
	if err != nil || result.Receipt == nil || result.Receipt.Outcome != OutcomeNoOp {
		t.Fatalf("GetRequestResult = %+v, %v", result, err)
	}
	_, err = h.session.SetDestination(h.req(), SetDestinationParams{PortalID: id, Destination: localApp(0)})
	wantCode(t, err, CodeInvalidRequest)
}

func TestDestinationKindTransitions(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	for _, d := range []portal.Destination{remoteApp("app.example.com", 8443), localApp(8787), remoteApp("10.0.0.5", 80)} {
		if _, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: id, Destination: d}); err != nil {
			t.Fatalf("SetDestination(%+v): %v", d, err)
		}
		if got := h.portal(id); got.Destination != d || got.ID != id || got.Name != "hermes" {
			t.Fatalf("Portal = %+v, want destination %+v with the same identity", got, d)
		}
	}
}

func TestPublicModeDestinationRestrictions(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	receipt, err := h.session.CreatePortal(h.req(), CreatePortalParams{Name: "public", Destination: localApp(8787), PublicAccess: portal.PublicAccessModePublic})
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.PortalID
	if p := h.portal(id); p.Availability[OpSelectRemoteApp].Available {
		t.Fatal("a public Portal offers Remote App")
	}
	_, err = h.session.SetDestination(h.req(), SetDestinationParams{PortalID: id, Destination: remoteApp("app.example.com", 443)})
	wantCode(t, err, CodeNotAvailable)
	if _, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: id, Destination: localApp(9000)}); err != nil {
		t.Fatalf("a public Portal must accept another Local App: %v", err)
	}
}

func TestAuthoritativeOperationMatrix(t *testing.T) {
	failRemove := errors.New("remove failed")
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	enabled := h.create("enabled", localApp(1))
	stopped := h.create("stopped", localApp(2))
	if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: stopped, State: portal.DesiredStateStopped}); err != nil {
		t.Fatal(err)
	}
	removing := h.create("removing", localApp(3))
	h.factory.set(func(f *fakeFactory) { f.removeErr = failRemove })
	if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: removing, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	h.settle()

	want := map[string]map[Operation]Availability{
		enabled: {
			OpStart: {Reason: CodeNotAvailable}, OpStop: {Available: true}, OpSetDestination: {Available: true},
			OpSelectRemoteApp: {Available: true}, OpRemovePortal: {Available: true}, OpRetryRemoval: {Reason: CodeNotAvailable},
		},
		stopped: {
			OpStart: {Available: true}, OpStop: {Reason: CodeNotAvailable}, OpSetDestination: {Available: true},
			OpRemovePortal: {Available: true}, OpRetryRemoval: {Reason: CodeNotAvailable},
		},
		removing: {
			OpStart: {Reason: CodeNotAvailable}, OpStop: {Reason: CodeNotAvailable}, OpSetDestination: {Reason: CodeNotAvailable},
			OpSelectRemoteApp: {Reason: CodeNotAvailable}, OpRemovePortal: {Reason: CodeNotAvailable}, OpRetryRemoval: {Available: true},
		},
	}
	for id, ops := range want {
		p := h.portal(id)
		for op, a := range ops {
			if p.Availability[op] != a {
				t.Errorf("%s %s = %+v, want %+v", p.Name, op, p.Availability[op], a)
			}
		}
	}
	if p := h.portal(removing); p.Problem != ProblemRemovalFailed || p.Lifecycle != installation.LifecyclePendingRemoval {
		t.Fatalf("removing Portal = %+v", p)
	}
	installationWant := map[Operation]Availability{
		OpCreatePortal: {Available: true}, OpSetOperationalLogging: {Available: true}, OpSetLaunchAtLoginOffer: {Available: true},
		OpResetTailnet: {Reason: CodeNotAvailable}, OpRetryRuntime: {Reason: CodeNotAvailable}, OpDiscoverLocalApps: {Reason: CodeCapabilityUnavailable},
	}
	for op, a := range installationWant {
		if got := h.session.GetSnapshot().Availability[op]; got != a {
			t.Errorf("installation %s = %+v, want %+v", op, got, a)
		}
	}

	// A stale client hint is rechecked server-side.
	_, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: removing, Confirmed: true})
	wantCode(t, err, CodeNotAvailable)
	_, err = h.session.RetryRemoval(h.req(), RetryRemovalParams{PortalID: enabled})
	wantCode(t, err, CodeNotAvailable)
	_, err = h.session.RetryRuntime(h.req())
	wantCode(t, err, CodeNotAvailable)
	_, err = h.session.ResetTailnet(h.req(), ResetTailnetParams{Confirmed: true})
	wantCode(t, err, CodeNotAvailable)

	// A Portal awaiting rejection cleanup offers nothing.
	rt := h.factory.runtime()
	rt.emitStatus(enabled, online("enabled", "one.ts.net", "tailnet-a"))
	h.factory.set(func(f *fakeFactory) { f.cleanupErr = errors.New("cleanup failed") })
	rt.emitStatus(stopped, online("stopped", "two.ts.net", "tailnet-b"))
	h.settle()
	rejected := h.portal(stopped)
	if rejected.Lifecycle != installation.LifecyclePendingTailnetRejection {
		t.Fatalf("Portal = %+v, want pending rejection", rejected)
	}
	for op, a := range rejected.Availability {
		if a.Available {
			t.Errorf("pending-rejection Portal offers %s", op)
		}
	}

	// With no runtime, a failed removal reports why it cannot be retried.
	h.factory.set(func(f *fakeFactory) { f.buildErr = errors.New("no runtime") })
	h.chooseLogging(installation.LoggingDisabled)
	if got := h.portal(removing).Availability[OpRetryRemoval]; got.Reason != CodeRuntimeUnavailable {
		t.Fatalf("retryRemoval without a runtime = %+v", got)
	}
	if got := h.session.GetSnapshot().Availability[OpRetryRuntime]; !got.Available {
		t.Fatalf("retryRuntime = %+v, want available", got)
	}
}

func TestDurableRemovalAndRetry(t *testing.T) {
	failRemove := errors.New("remove failed")
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	h.factory.runtime().emitStatus(id, online("hermes", "one.ts.net", "tailnet-a"))
	h.factory.set(func(f *fakeFactory) { f.removeErr = failRemove })

	_, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: id})
	wantCode(t, err, CodeConfirmationRequired)
	if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: id, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	removes := h.factory.callsNamed("remove:" + id)
	if len(removes) != 1 {
		t.Fatalf("RemovePortal calls = %d", len(removes))
	}
	at := removes[0].state.Installation.Portals[0]
	if at.Lifecycle != installation.LifecyclePendingRemoval || at.RemovalAssignedName == nil || *at.RemovalAssignedName != "hermes" {
		t.Fatalf("on-disk record at RemovePortal = %+v", at)
	}
	reconciles := h.factory.callsNamed("reconcile")
	last := reconciles[len(reconciles)-1].state
	if last.Installation.Portals[0].Lifecycle != installation.LifecyclePendingRemoval || len(h.factory.runtime().lastReconcile()) != 0 {
		t.Fatal("removal ran before a reconcile excluded the durable removing Portal")
	}
	if p := h.portal(id); p == nil || p.Problem != ProblemRemovalFailed || !p.Availability[OpRetryRemoval].Available {
		t.Fatalf("failed removal = %+v", p)
	}

	// A restart gives one automatic attempt; the record survives its failure.
	h.restart()
	if n := len(h.factory.callsNamed("remove:" + id)); n != 2 {
		t.Fatalf("RemovePortal calls after restart = %d, want 2", n)
	}
	h.settle()
	if n := len(h.factory.callsNamed("remove:" + id)); n != 2 {
		t.Fatal("a failed removal retried without retryRemoval")
	}

	// The record leaves only after a durable save.
	failSave := false
	h.factory.set(func(f *fakeFactory) {
		f.removeErr = nil
		f.onRemove = func() { failSave = true }
	})
	h.stop()
	h.storeOpt = Options{Hook: func(p Phase) error {
		if failSave && p == PhaseCommitRename {
			return errors.New("injected")
		}
		return nil
	}}
	h.start()
	if p := h.portal(id); p == nil || p.Problem != ProblemRemovalFailed {
		t.Fatalf("record after a failed final save = %+v", p)
	}
	h.storeOpt = Options{}
	h.factory.set(func(f *fakeFactory) { f.onRemove = nil; f.removeErr = failRemove })
	h.restart()

	h.factory.set(func(f *fakeFactory) { f.removeErr = nil })
	if _, err := h.session.RetryRemoval(h.req(), RetryRemovalParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.portal(id) != nil || len(h.disk().Installation.Portals) != 0 {
		t.Fatal("retried removal did not drop the record")
	}
	for _, rt := range h.factory.runtimes[1:] {
		for _, configs := range rt.reconciles {
			if slices.ContainsFunc(configs, func(c portal.Config) bool { return c.ID == id }) {
				t.Fatal("a retry re-enabled the removing Portal")
			}
		}
	}
}

func TestPendingCleanupRetainsRecord(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	keep := h.create("keep", localApp(1))
	rejected := h.create("rejected", localApp(2))
	rt := h.factory.runtime()
	rt.emitStatus(keep, online("keep", "one.ts.net", "tailnet-a"))
	h.factory.set(func(f *fakeFactory) { f.cleanupErr = errors.New("cleanup failed") })
	rt.emitStatus(rejected, online("rejected", "two.ts.net", "tailnet-b"))
	h.settle()
	if p := h.portal(rejected); p == nil || p.Lifecycle != installation.LifecyclePendingTailnetRejection || p.Problem != ProblemCleanupFailed {
		t.Fatalf("rejected Portal after failed cleanup = %+v", p)
	}
	h.factory.set(func(f *fakeFactory) { f.cleanupErr = nil })
	h.restart()
	if h.portal(rejected) != nil {
		t.Fatal("pending cleanup did not resume after restart")
	}
	if h.portal(keep) == nil {
		t.Fatal("cleanup removed an unrelated Portal")
	}
}

func TestRejectionCleanupAlertsAndReset(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	keep := h.create("keep", localApp(1))
	rejected := h.create("rejected", localApp(2))
	rt := h.factory.runtime()

	rt.emitStatus(keep, online("keep", "one.ts.net", "tailnet-a"))
	if b := h.disk().Installation.Binding; b == nil || b.Name != "tailnet-a" || b.MagicDNSSuffix != "one.ts.net" {
		t.Fatalf("binding = %+v", b)
	}
	rt.emitStatus(rejected, online("rejected", "two.ts.net", "tailnet-b"))
	h.settle()
	cleanups := h.factory.callsNamed("cleanup:" + rejected)
	if len(cleanups) != 1 {
		t.Fatalf("cleanup calls = %d", len(cleanups))
	}
	if i := portalIndex(&cleanups[0].state, rejected); i < 0 || cleanups[0].state.Installation.Portals[i].Lifecycle != installation.LifecyclePendingTailnetRejection {
		t.Fatal("cleanup ran before the rejection was durable")
	}
	if last := rt.lastReconcile(); len(last) != 1 || last[0].ID != keep {
		t.Fatalf("reconcile after rejection = %+v, want only the unrelated Portal", last)
	}
	snapshot := h.session.GetSnapshot()
	if len(snapshot.Alerts) != 1 || h.portal(rejected) != nil {
		t.Fatalf("snapshot after cleanup = %+v", snapshot)
	}
	alert := snapshot.Alerts[0]
	if alert.PortalName != "rejected" || *alert.AssignedName != "rejected" || *alert.ExpectedMagicDNSSuffix != "one.ts.net" || *alert.RejectedMagicDNSSuffix != "two.ts.net" {
		t.Fatalf("alert = %+v", alert)
	}
	if !snapshot.TailnetBound || snapshot.TailnetMagicDNSSuffix != "one.ts.net" {
		t.Fatalf("binding facts = %+v", snapshot)
	}

	if _, err := h.session.DismissAlert(h.req(), DismissAlertParams{AlertID: alert.ID}); err != nil {
		t.Fatal(err)
	}
	if len(h.disk().Installation.Alerts) != 0 {
		t.Fatal("alert not dismissed")
	}
	_, err := h.session.DismissAlert(h.req(), DismissAlertParams{AlertID: alert.ID})
	wantCode(t, err, CodeNotFound)

	_, err = h.session.ResetTailnet(h.req(), ResetTailnetParams{Confirmed: true})
	wantCode(t, err, CodeNotAvailable)
	if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: keep, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	_, err = h.session.ResetTailnet(h.req(), ResetTailnetParams{})
	wantCode(t, err, CodeConfirmationRequired)
	if _, err := h.session.ResetTailnet(h.req(), ResetTailnetParams{Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if h.disk().Installation.Binding != nil {
		t.Fatal("binding not reset")
	}
}

func TestRejectionEvidenceIsSanitized(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	keep := h.create("keep", localApp(1))
	rejected := h.create("rejected", localApp(2))
	rt := h.factory.runtime()
	rt.emitStatus(keep, online("keep", "one.ts.net", "tailnet-a"))
	hostile := online("Bad Name", "two.ts.net", "tailnet-b")
	hostile.MagicDNSSuffix = "evil.example"
	rt.emitStatus(rejected, hostile)
	h.settle()
	alerts := h.disk().Installation.Alerts
	if len(alerts) != 1 || alerts[0].AssignedName != nil || alerts[0].RejectedMagicDNSSuffix != nil {
		t.Fatalf("alert evidence = %+v", alerts)
	}
}

func TestLoginOfferTransitionsAndRecovery(t *testing.T) {
	type step = installation.LaunchAtLoginOffer
	const (
		notOffered = installation.OfferNotOffered
		presented  = installation.OfferPresented
		accepted   = installation.OfferAccepted
		declined   = installation.OfferDeclined
	)
	for _, tc := range []struct {
		from, to step
		want     Outcome
	}{
		{notOffered, presented, OutcomeAccepted}, {notOffered, accepted, OutcomeAccepted}, {notOffered, declined, ""},
		{presented, accepted, OutcomeAccepted}, {presented, declined, OutcomeAccepted}, {presented, presented, OutcomeNoOp},
		{declined, accepted, OutcomeAccepted}, {declined, presented, ""}, {declined, declined, OutcomeNoOp},
		{accepted, presented, ""}, {accepted, declined, ""}, {accepted, accepted, OutcomeNoOp},
	} {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			h := newHarness(t)
			path := map[step][]step{notOffered: nil, presented: {presented}, accepted: {accepted}, declined: {presented, declined}}
			for _, s := range path[tc.from] {
				if _, err := h.session.SetLaunchAtLoginOffer(h.req(), SetLaunchAtLoginOfferParams{State: s}); err != nil {
					t.Fatal(err)
				}
			}
			req := h.req()
			receipt, err := h.session.SetLaunchAtLoginOffer(req, SetLaunchAtLoginOfferParams{State: tc.to})
			if tc.want == "" {
				wantCode(t, err, CodeNotAvailable)
				if h.disk().Installation.LaunchAtLoginOffer != tc.from {
					t.Fatal("an invalid transition changed the offer")
				}
				return
			}
			if err != nil || receipt.Outcome != tc.want || h.disk().Installation.LaunchAtLoginOffer != tc.to {
				t.Fatalf("SetLaunchAtLoginOffer = %+v, %v", receipt, err)
			}
			// Same-ID recovery returns the original receipt, also after a restart.
			h.restart()
			again, err := h.session.SetLaunchAtLoginOffer(req, SetLaunchAtLoginOfferParams{State: tc.to})
			if err != nil || again != receipt {
				t.Fatalf("recovery = %+v, %v; want %+v", again, err, receipt)
			}
		})
	}
	t.Run("not offered on this service", func(t *testing.T) {
		h := newHarness(t, func(h *harness) { h.opts.LaunchAtLoginOfferCapable = false })
		_, err := h.session.SetLaunchAtLoginOffer(h.req(), SetLaunchAtLoginOfferParams{State: presented})
		wantCode(t, err, CodeCapabilityUnavailable)
		if h.session.GetSnapshot().Availability[OpSetLaunchAtLoginOffer].Reason != CodeCapabilityUnavailable {
			t.Fatal("availability does not report the missing capability")
		}
	})
	t.Run("notOffered is not a requestable state", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.session.SetLaunchAtLoginOffer(h.req(), SetLaunchAtLoginOfferParams{State: notOffered})
		wantCode(t, err, CodeInvalidRequest)
	})
}

func TestLoggingRuntimeReconfigurationAndFailure(t *testing.T) {
	t.Run("preference commits before the old runtime closes", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		id := h.create("hermes", localApp(1))
		h.chooseLogging(installation.LoggingDisabled)
		closes := h.factory.callsNamed("close")
		if len(closes) != 1 || closes[0].state.Installation.Logging != installation.LoggingDisabled {
			t.Fatalf("close calls = %+v", closes)
		}
		names := h.factory.callNames()
		if i, j := slices.Index(names, "close"), slices.Index(names, "build:disabled"); i < 0 || j < i {
			t.Fatalf("calls = %v, want close before the new build", names)
		}
		if last := h.factory.runtime().lastReconcile(); h.factory.runtime().logging != installation.LoggingDisabled || len(last) != 1 || last[0].ID != id {
			t.Fatal("the new runtime was not reconciled with the new logging value")
		}
		// Choosing the current value is a no-op and leaves the runtime alone.
		receipt, err := h.session.SetOperationalLogging(h.req(), SetOperationalLoggingParams{Logging: installation.LoggingDisabled})
		h.settle()
		if err != nil || receipt.Outcome != OutcomeNoOp || len(h.factory.callsNamed("close")) != 1 {
			t.Fatalf("same-value logging = %+v, %v", receipt, err)
		}
	})
	t.Run("a commit failure leaves the runtime untouched", func(t *testing.T) {
		var fail bool
		h := newHarness(t, func(h *harness) {
			h.storeOpt = Options{Hook: func(p Phase) error {
				if fail && p == PhaseCommitWrite {
					return errors.New("injected")
				}
				return nil
			}}
		})
		h.chooseLogging(installation.LoggingEnabled)
		fail = true
		_, err := h.session.SetOperationalLogging(h.req(), SetOperationalLoggingParams{Logging: installation.LoggingDisabled})
		wantCode(t, err, CodePersistenceUnavailable)
		h.settle()
		if len(h.factory.callsNamed("close")) != 0 || len(h.factory.callsNamed("build:disabled")) != 0 {
			t.Fatal("the runtime was reconfigured without a durable preference")
		}
	})
	t.Run("factory failure is visible and recoverable", func(t *testing.T) {
		h := newHarness(t)
		h.factory.set(func(f *fakeFactory) { f.buildErr = errors.New("no runtime") })
		h.chooseLogging(installation.LoggingEnabled)
		snapshot := h.session.GetSnapshot()
		if snapshot.Service.Runtime != runtimeUnavailable || !snapshot.Availability[OpRetryRuntime].Available {
			t.Fatalf("snapshot after factory failure = %+v", snapshot.Service)
		}
		h.factory.set(func(f *fakeFactory) { f.buildErr = nil })
		if _, err := h.session.RetryRuntime(h.req()); err != nil {
			t.Fatal(err)
		}
		h.settle()
		if h.session.GetSnapshot().Service.Runtime != runtimeRunning {
			t.Fatal("retryRuntime did not rebuild the runtime")
		}
	})
	t.Run("a failed close blocks replacement", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		h.factory.set(func(f *fakeFactory) { f.closeErr = errors.New("still running") })
		h.chooseLogging(installation.LoggingDisabled)
		if len(h.factory.callsNamed("build:disabled")) != 0 || h.session.GetSnapshot().Service.Runtime != runtimeUnavailable {
			t.Fatal("a new runtime started while the old one was not proven closed")
		}
		h.factory.set(func(f *fakeFactory) { f.closeErr = nil })
		if _, err := h.session.RetryRuntime(h.req()); err != nil {
			t.Fatal(err)
		}
		h.settle()
		if len(h.factory.callsNamed("close")) != 2 || len(h.factory.callsNamed("build:disabled")) != 1 {
			t.Fatalf("calls = %v, want the close retried before the rebuild", h.factory.callNames())
		}
	})
}

func TestRevertedLoggingStillRetriesTheFailedClose(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	old := h.factory.runtime()
	h.factory.set(func(f *fakeFactory) { f.closeErr = errors.New("still running") })
	h.chooseLogging(installation.LoggingDisabled)
	// Reverting the choice must not hand the half-closed runtime back.
	h.chooseLogging(installation.LoggingEnabled)
	if got := h.session.GetSnapshot().Service.Runtime; got != runtimeUnavailable {
		t.Fatalf("runtime after a failed close and a reverted choice = %q", got)
	}
	h.factory.set(func(f *fakeFactory) { f.closeErr = nil })
	if _, err := h.session.RetryRuntime(h.req()); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if n := len(h.factory.callsNamed("close")); n != 2 {
		t.Fatalf("close calls = %d, want the failed close retried", n)
	}
	rt := h.factory.runtime()
	if rt == old || rt.logging != installation.LoggingEnabled || h.session.GetSnapshot().Service.Runtime != runtimeRunning {
		t.Fatal("no fresh runtime replaced the closed one")
	}
	if last := rt.lastReconcile(); len(last) != 1 || last[0].ID != id {
		t.Fatalf("new runtime reconciled %+v", last)
	}
}

func TestRealRuntimeFactorySetsLoggingKnobBeforeNewRuntime(t *testing.T) {
	original := newPortalRuntime
	defer func() { newPortalRuntime = original }()
	t.Setenv(noLogsKnob, "")
	var seen []string
	newPortalRuntime = func(string) Runtime {
		value, set := os.LookupEnv(noLogsKnob)
		if !set {
			value = "<unset>"
		}
		seen = append(seen, value)
		return nil
	}
	for _, logging := range []installation.Logging{installation.LoggingDisabled, installation.LoggingEnabled} {
		if _, err := RealRuntimeFactory(t.TempDir(), logging); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(seen, []string{"true", "<unset>"}) {
		t.Fatalf("knob at NewRuntime = %v", seen)
	}
	if _, err := RealRuntimeFactory(t.TempDir(), installation.LoggingUndecided); err == nil {
		t.Fatal("the real factory started a runtime without a logging choice")
	}
}
