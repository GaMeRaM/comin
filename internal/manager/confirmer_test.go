package manager

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nlewo/comin/internal/broker"
	"github.com/stretchr/testify/assert"
)

func TestConfirmerSubmit(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Manual, time.Second, "")
	c.Start()
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "", c.status().Submitted)
	}, 3*time.Second, 100*time.Millisecond)

	c.Submit("uuid1")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "uuid1", c.status().Submitted)
	}, 1*time.Second, 100*time.Millisecond)
}

func TestConfirmerManual(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Manual, time.Second, "")
	go func() {
		<-c.confirmed
	}()
	c.Start()
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "", c.status().Submitted)
	}, 3*time.Second, 100*time.Millisecond)

	c.Submit("uuid1")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "uuid1", c.status().Submitted)
		assert.Equal(ct, "", c.status().Confirmed)
	}, 1*time.Second, 100*time.Millisecond)
}

func TestConfirmerWithout(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Without, 0, "")
	var expectedUuid atomic.Bool
	go func() {
		t := <-c.confirmed
		if t == "uuid1" {
			expectedUuid.Store(true)
		}
	}()
	c.Start()
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "", c.status().Submitted)
	}, 3*time.Second, 100*time.Millisecond)

	c.Submit("uuid1")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, expectedUuid.Load())
	}, 1*time.Second, 100*time.Millisecond)
}

func TestConfirmerAuto(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Auto, 2*time.Second, "")
	var expectedUuid atomic.Bool
	go func() {
		t := <-c.confirmed
		if t == "uuid1" {
			expectedUuid.Store(true)
		}
	}()
	c.Start()
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "", c.status().Submitted)
	}, 1*time.Second, 100*time.Millisecond)

	c.Submit("uuid1")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "uuid1", c.status().Submitted)
		assert.Equal(ct, "", c.status().Confirmed)
	}, 1*time.Second, 100*time.Millisecond)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, expectedUuid.Load())
	}, 3*time.Second, 100*time.Millisecond)
}

func TestConfirmerAutoCancel(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Auto, 2*time.Second, "")
	go func() {
		<-c.confirmed
	}()
	c.Start()
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "", c.status().Submitted)
	}, 1*time.Second, 100*time.Millisecond)

	c.Submit("uuid1")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "uuid1", c.status().Submitted)
		assert.True(ct, c.status().AutoconfirmStarted.GetValue())
	}, 1*time.Second, 100*time.Millisecond)

	c.Cancel()
	assert.Never(t, func() bool {
		return c.status().Confirmed == "uuid1"
	}, 3*time.Second, 100*time.Millisecond)
}

func TestConfirmerResubmit(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Auto, 3*time.Second, "")
	var expectedUuid atomic.Bool
	go func() {
		t := <-c.confirmed
		if t == "uuid2" {
			expectedUuid.Store(true)
		}
	}()
	c.Start()
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "", c.status().Submitted)
	}, 1*time.Second, 100*time.Millisecond)

	c.Submit("uuid1")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, "uuid1", c.status().Submitted)
		assert.True(ct, c.status().AutoconfirmStarted.GetValue())
	}, 1*time.Second, 100*time.Millisecond)
	assert.Never(t, func() bool {
		return c.status().Confirmed == "uuid1"
	}, 1*time.Second, 100*time.Millisecond)

	c.Submit("uuid2")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, c.status().AutoconfirmStarted.GetValue())
	}, 1*time.Second, 100*time.Millisecond)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, expectedUuid.Load())
	}, 4*time.Second, 100*time.Millisecond)
}

func TestConfirmerConfirmBeforeSubmit(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Auto, 3*time.Second, "")
	var expectedUuid atomic.Bool
	go func() {
		t := <-c.confirmed
		if t == "uuid1" {
			expectedUuid.Store(true)
		}
	}()
	c.Start()
	c.Confirm("uuid1")
	assert.Never(t, func() bool {
		return expectedUuid.Load()
	}, 1*time.Second, 100*time.Millisecond)
	c.Submit("uuid1")
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, expectedUuid.Load())
	}, 4*time.Second, 100*time.Millisecond)
}

func TestConfirmCurrentRejectsStaleConsent(t *testing.T) {
	bk := broker.New()
	bk.Start()
	c := NewConfirmer(bk, Manual, 0, "deploy")
	c.Start()
	assert.Error(t, c.ConfirmCurrent("b"))
	c.Submit("c")
	assert.Error(t, c.ConfirmCurrent("b"))
	assert.Equal(t, "c", c.status().Submitted)
	assert.Empty(t, c.status().Confirmed)
	assert.NoError(t, c.ConfirmCurrent("c"))
	select {
	case uuid := <-c.confirmed:
		assert.Equal(t, "c", uuid)
	case <-time.After(time.Second):
		t.Fatal("confirmation missing")
	}
	assert.Error(t, c.ConfirmCurrent("c"))
}

// The manager can withdraw a testing release while an approval is waiting for
// it. The confirmer must keep processing commands instead of blocking on send.
func TestWithdrawWhileApprovalAwaitsManager(t *testing.T) {
	b := broker.New()
	b.Start()
	c := NewConfirmer(b, Manual, 0, "deploy")
	c.Start()
	c.Submit("old-test")
	assert.NoError(t, c.ConfirmCurrent("old-test"))
	withdrawn := make(chan struct{})
	go func() { c.Withdraw(); close(withdrawn) }()
	select {
	case <-withdrawn:
	case <-time.After(time.Second):
		t.Fatal("withdrawal deadlocked with an undelivered approval")
	}
	assert.Empty(t, c.status().Submitted)
	c.Submit("main")
	assert.Error(t, c.ConfirmCurrent("old-test"))
	assert.NoError(t, c.ConfirmCurrent("main"))
	select {
	case uuid := <-c.confirmed:
		assert.Equal(t, "main", uuid)
	case <-time.After(time.Second):
		t.Fatal("main approval was not delivered")
	}
}
