package mygoui

import (
	"image"
	"image/draw"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func copyProjectHoverImage(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

func projectHoverPixelsDiffer(left, right *image.RGBA, box ui.Rect) bool {
	area := image.Rect(int(box.X), int(box.Y), int(box.X+box.W), int(box.Y+box.H)).Intersect(left.Bounds()).Intersect(right.Bounds())
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if left.RGBAAt(x, y) != right.RGBAAt(x, y) {
				return true
			}
		}
	}
	return false
}

func TestProjectAddButtonsShowOnlyOnTheirOwnHover(t *testing.T) {
	a := testApp()
	a.projects = append(a.projects, appsession.ProjectFolder{Path: "/tmp/other-project", Name: "other-project"})
	tt := ui.NewTester(a.sidebar, 340, 700)
	tt.Move(1, 1)
	addProject, ok := tt.Find("Add project folder")
	if !ok {
		t.Fatal("add project control is not accessible")
	}
	addSession, ok := tt.Find("Create session in project")
	if !ok {
		t.Fatal("add session control is not accessible")
	}
	otherSession, ok := tt.Find("Create session in other-project")
	if !ok {
		t.Fatal("second project control is not accessible")
	}
	projectsLabel, ok := tt.Find("Projects")
	if !ok {
		t.Fatal("Projects heading is missing")
	}
	projectLabel, ok := tt.Find("project")
	if !ok {
		t.Fatal("project row is missing")
	}
	otherLabel, ok := tt.Find("other-project")
	if !ok {
		t.Fatal("second project row is missing")
	}
	atRest := copyProjectHoverImage(tt.Image())
	tt.Move(projectsLabel.X+projectsLabel.W/2, projectsLabel.Y+projectsLabel.H/2)
	headerHover := copyProjectHoverImage(tt.Image())
	if !projectHoverPixelsDiffer(atRest, headerHover, addProject) || projectHoverPixelsDiffer(atRest, headerHover, addSession) || projectHoverPixelsDiffer(atRest, headerHover, otherSession) {
		t.Fatal("heading hover did not reveal only the add-project button")
	}
	tt.Move(projectLabel.X+projectLabel.W/2, projectLabel.Y+projectLabel.H/2)
	rowHover := copyProjectHoverImage(tt.Image())
	if projectHoverPixelsDiffer(atRest, rowHover, addProject) || !projectHoverPixelsDiffer(atRest, rowHover, addSession) || projectHoverPixelsDiffer(atRest, rowHover, otherSession) {
		t.Fatal("project hover did not reveal only that project's add-session button")
	}
	tt.Move(otherLabel.X+otherLabel.W/2, otherLabel.Y+otherLabel.H/2)
	otherHover := copyProjectHoverImage(tt.Image())
	if projectHoverPixelsDiffer(atRest, otherHover, addProject) || projectHoverPixelsDiffer(atRest, otherHover, addSession) || !projectHoverPixelsDiffer(atRest, otherHover, otherSession) {
		t.Fatal("hovering another project did not hide the first project's button")
	}
	tt.Move(1, 1)
	if projectHoverPixelsDiffer(atRest, tt.Image(), addProject) || projectHoverPixelsDiffer(atRest, tt.Image(), addSession) || projectHoverPixelsDiffer(atRest, tt.Image(), otherSession) {
		t.Fatal("buttons remained visible after the pointer left")
	}
}

func TestProjectAddButtonAppearsWithKeyboardFocus(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.sidebar, 340, 700)
	tt.Move(1, 1)
	box, ok := tt.Find("Add project folder")
	if !ok {
		t.Fatal("add project control is not accessible")
	}
	before := copyProjectHoverImage(tt.Image())
	for i := 0; i < 10 && !tt.Focused("Add project folder"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused("Add project folder") || !projectHoverPixelsDiffer(before, tt.Image(), box) {
		t.Fatal("add project button did not become visible on keyboard focus")
	}
}

func TestHoveredProjectAddButtonTargetsThatProject(t *testing.T) {
	a := testApp()
	a.fileSaving = true // Stop before creating a real session or changing projects.
	a.projects = append(a.projects, appsession.ProjectFolder{Path: "/tmp/other-project", Name: "other-project"})
	tt := ui.NewTester(a.sidebar, 340, 700)
	label, ok := tt.Find("other-project")
	if !ok {
		t.Fatal("second project row is missing")
	}
	tt.Move(label.X+label.W/2, label.Y+label.H/2)
	if err := tt.Click("Create session in other-project"); err != nil {
		t.Fatal(err)
	}
	if !a.bannerErr || !strings.Contains(a.banner, "Save or discard") {
		t.Fatalf("click did not target the other project: %q", a.banner)
	}
}
