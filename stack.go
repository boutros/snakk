package main

// Stack is a FIFO stack of chatLines implemented as a ring buffer, so
// that it always keep the latest <size> messages.
type Stack struct {
	items  []chatLine
	oldest int  // oldest item in buffer
	next   int  // next write mark
	size   int  // current size of items
	full   bool // true when buffer is full
}

// newStack creates a new Stack queue of max size chatlines.
func newStack(size int) *Stack {
	return &Stack{items: make([]chatLine, size)}
}

// Push an chatline intem to the queue.
func (q *Stack) Push(l chatLine) {
	// assign item
	q.items[q.next] = l

	// increase size if still below max size
	if !q.full {
		q.size = q.size + 1
	}

	if q.full {
		// move read marker ahead
		if q.oldest < len(q.items)-1 {
			q.oldest = q.oldest + 1
		} else {
			// or back to start if end is reached
			q.oldest = 0
		}
	}

	// move write mark
	if q.next < len(q.items)-1 {
		q.next = q.next + 1
	} else {
		// back to start of buffer
		q.next = 0
		q.full = true
	}

}

// All returns all chatLine items, in cronological order.
func (q *Stack) All() []chatLine {
	all := make([]chatLine, q.size)

	if q.next >= q.next && !q.full {
		copy(all, q.items[q.oldest:q.size])
	} else {
		copy(all, q.items[q.oldest:q.size])
		copy(all[len(q.items[q.oldest:q.size]):q.size], q.items[0:q.next])
	}
	return all
}
