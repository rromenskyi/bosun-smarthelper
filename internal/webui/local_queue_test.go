package webui

import "testing"

func TestLocalQueueJoinGrantsImmediatelyWhenFree(t *testing.T) {
	var q localQueue
	turn, position := q.join()
	if position != 0 {
		t.Fatalf("position = %d, want 0 (slot was free)", position)
	}
	select {
	case <-turn:
	default:
		t.Fatal("turn channel should already be signaled when the slot was free")
	}
}

func TestLocalQueueOrdersWaitersFIFO(t *testing.T) {
	var q localQueue
	first, pos := q.join()
	if pos != 0 {
		t.Fatalf("first position = %d, want 0", pos)
	}
	second, pos := q.join()
	if pos != 1 {
		t.Fatalf("second position = %d, want 1", pos)
	}
	third, pos := q.join()
	if pos != 2 {
		t.Fatalf("third position = %d, want 2", pos)
	}
	<-first // first's turn already granted

	q.release()
	select {
	case <-second:
	default:
		t.Fatal("second should be granted its turn after release")
	}
	select {
	case <-third:
		t.Fatal("third must not be granted while second is still holding")
	default:
	}

	q.release()
	select {
	case <-third:
	default:
		t.Fatal("third should be granted its turn after second releases")
	}
}

func TestLocalQueueAbandonBeforeGrant(t *testing.T) {
	var q localQueue
	holder, _ := q.join()
	<-holder
	waiter, position := q.join()
	if position != 1 {
		t.Fatalf("position = %d, want 1", position)
	}

	q.abandon(waiter) // gives up before its turn arrives

	q.release() // holder finishes
	select {
	case <-waiter:
		t.Fatal("abandoned waiter must not be granted a turn")
	default:
	}
	// The slot should be free again, not stuck waiting on the abandoned waiter.
	next, position := q.join()
	if position != 0 {
		t.Fatalf("position = %d, want 0 (slot should be free after the only waiter left)", position)
	}
	select {
	case <-next:
	default:
		t.Fatal("a fresh joiner should be granted the turn immediately")
	}
}

func TestLocalQueueAbandonAfterGrantRace(t *testing.T) {
	var q localQueue
	holder, _ := q.join()
	<-holder
	waiter, _ := q.join()

	// Simulates the real race in handleChat's select: turn (waiter) becomes
	// ready right as the caller's ctx also expires, and Go's select happens
	// to pick ctx.Done() — so waiter is never drained by the caller before
	// abandon is called with it.
	q.release()
	q.abandon(waiter) // must notice the unclaimed grant and pass it on

	next, position := q.join()
	if position != 0 {
		t.Fatalf("position = %d, want 0 (abandon-after-grant must release the slot)", position)
	}
	select {
	case <-next:
	default:
		t.Fatal("a fresh joiner should be granted the turn after an abandoned-post-grant waiter")
	}
}

func TestLocalQueueTryHold(t *testing.T) {
	var q localQueue
	if !q.tryHold() {
		t.Fatal("tryHold on a free queue should succeed")
	}
	if q.tryHold() {
		t.Fatal("tryHold while already held should fail")
	}
	q.release()
	if !q.tryHold() {
		t.Fatal("tryHold after release should succeed again")
	}
}
