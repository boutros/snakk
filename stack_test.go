package main

import "testing"

func TestStack(t *testing.T) {
	q := newStack(3)

	a := chatLine{Message: "a"}
	b := chatLine{Message: "b"}
	c := chatLine{Message: "c"}
	d := chatLine{Message: "d"}
	e := chatLine{Message: "e"}
	f := chatLine{Message: "f"}
	g := chatLine{Message: "g"}

	if len(q.items) != 3 {
		t.Errorf("len(newStack(3)) => %d; want 3", len(q.items))
	}

	if q.size != 0 {
		t.Errorf("empty queue should have size 0")
	}

	q.Push(a)
	if q.size != 1 {
		t.Errorf("queue with one item should have size 1")
	}

	q.Push(b)

	all := q.All()
	if len(all) != 2 {
		t.Errorf("len(q.All()) => %d; want 2", len(all))
	}

	if all[0].Message != "a" {
		t.Errorf("q.All() returns items in wrong order")
	}

	if all[1].Message != "b" {
		t.Errorf("q.All() returns items in wrong order")
	}

	q.Push(c)
	q.Push(d)

	all = q.All()
	if len(all) != 3 {
		t.Errorf("len(q.All()) => %d; want 3", len(q.All()))
	}

	if all[0].Message != "b" {
		t.Errorf("old items not overwritten by new item")
	}

	if all[1].Message != "c" {
		t.Errorf("old items not overwritten by new item")
	}

	if all[2].Message != "d" {
		t.Errorf("old items not overwritten by new item")
	}

	q.Push(e)
	q.Push(f)
	q.Push(g)

	all = q.All()

	if all[0].Message != "e" {
		t.Errorf("old items not overwritten by new item")
	}

	if all[1].Message != "f" {
		t.Errorf("old items not overwritten by new item")
	}

	if all[2].Message != "g" {
		t.Errorf("old items not overwritten by new item")
	}

}
