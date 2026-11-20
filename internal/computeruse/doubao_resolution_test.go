package computeruse

import (
	"context"
	"testing"
)

func TestResolveDoubaoWorkByInstalledIdentity(t *testing.T) {
	app := Application{ID: "opaque", Name: "豆包工作", Identity: "/Applications/DoubaoWork.app"}
	w := Target{ID: "right", Application: "DoubaoWork", ApplicationID: app.Identity}
	other := Target{ID: "other", Application: "DoubaoWork", ApplicationID: "/other/DoubaoWork.app"}
	for _, running := range []bool{false, true} {
		b := &applicationFixture{apps: []Application{app}, targets: []Target{other}, after: []Target{other, w}}
		if running {
			b.targets = append(b.targets, w)
		}
		r, err := ResolveApplication(context.Background(), b, "豆包work")
		if err != nil || !r.Matched || len(r.Targets) != 1 || r.Targets[0] != w || (!running) != (len(b.launched) == 1) {
			t.Fatalf("running=%v: %+v, launches=%v, err=%v", running, r, b.launched, err)
		}
	}
	b := &applicationFixture{apps: []Application{app, {ID: "ambiguous", Name: app.Name, Identity: other.ApplicationID}}}
	r, err := ResolveApplication(context.Background(), b, "豆包work")
	if err != nil || len(r.Applications) != 2 || len(b.launched) != 0 {
		t.Fatalf("ambiguous: %+v, %v", r, err)
	}
}
