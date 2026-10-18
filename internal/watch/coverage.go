package watch

import "sort"

// Coverage decides whether the stream at path is covered by a signed
// checkpoint, and says why not (F-018). The reason is printed in the mail, so
// it must be a fixed string of the gate, never text a producer wrote.
type Coverage func(path string) (bool, string)

// Uncovered is one stream file whose events were refused because no signed
// checkpoint covers it, as the mail needs it.
type Uncovered struct {
	File   string
	Reason string
	Count  int
}

// SetCoverage installs the checkpoint gate. Without one, every stream is read,
// as it was before the gate existed (the default, and what the switch leaves
// off).
func (w *Watcher) SetCoverage(c Coverage) { w.coverage = c }

// TakeUncovered returns the refused files since the last call, in a stable
// order, and forgets them. What was refused is never handed back as an event;
// this is how the caller learns it happened.
func (w *Watcher) TakeUncovered() []Uncovered {
	out := make([]Uncovered, 0, len(w.uncovered))
	for _, u := range w.uncovered {
		out = append(out, *u)
	}
	w.uncovered = nil
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

func (w *Watcher) noteUncovered(path, reason string) {
	w.NotCovered++
	if u, ok := w.uncovered[path]; ok {
		u.Count++
		return
	}
	if len(w.uncovered) >= maxPendingNotices {
		return
	}
	if w.uncovered == nil {
		w.uncovered = map[string]*Uncovered{}
	}
	w.uncovered[path] = &Uncovered{File: path, Reason: reason, Count: 1}
}
