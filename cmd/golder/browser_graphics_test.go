package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

const browserTestFrame = "\x1b_Ga=T,f=24,o=z,s=20,v=20,c=40,r=12,i=1,p=1,q=2,C=1,m=1;AAAA\x1b\\\x1b_Gm=0;BBBB\x1b\\"

func TestBrowserFrameAdapterOnlyInsideTuios(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"TUIOS_ENV": "1"}, true},
		{map[string]string{"TUIOS_PANE_HOSTED": "1"}, true},
		{map[string]string{"TUIOS_ENV": "0"}, false},
	} {
		var out bytes.Buffer
		writer := browserPaneOutput(&out, func(key string) string { return tc.env[key] })
		_, ok := writer.(*browserFrameWriter)
		if ok != tc.want {
			t.Fatalf("environment %v: adapted=%t, want %t", tc.env, ok, tc.want)
		}
	}
}

func TestBrowserFrameWriterSeparatesTransmitAndPlace(t *testing.T) {
	var out bytes.Buffer
	w := &browserFrameWriter{out: &out}
	n, err := w.Write([]byte(browserTestFrame))
	if err != nil || n != len(browserTestFrame) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	want := browserSyncBegin + strings.Replace(browserTestFrame, "a=T", "a=t", 1) +
		"\x1b_Ga=p,i=1,p=1,c=40,r=12,C=1,q=2\x1b\\" + browserSyncEnd
	if out.String() != want {
		t.Fatalf("frame transformation:\n got %q\nwant %q", out.String(), want)
	}
}

func TestBrowserFrameWriterLeavesOtherOutputAlone(t *testing.T) {
	for _, input := range []string{
		"normal browser chrome",
		"\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\",
		strings.Replace(browserTestFrame, "i=1,p=1", "i=2,p=1", 1),
		strings.Replace(browserTestFrame, "i=1,p=1", "i=1,p=2", 1),
		strings.Replace(browserTestFrame, "c=40", "c=0", 1),
		strings.Replace(browserTestFrame, "C=1", "C=0", 1),
		strings.Replace(browserTestFrame, "m=0", "m=1", 1),
		strings.TrimSuffix(browserTestFrame, "\x1b\\"),
	} {
		var out bytes.Buffer
		w := &browserFrameWriter{out: &out}
		n, err := w.Write([]byte(input))
		if err != nil || n != len(input) || out.String() != input {
			t.Fatalf("changed unrelated output %q: n=%d err=%v got=%q", input, n, err, out.String())
		}
	}
}

func TestBrowserFrameWriterSupportsSinglePacket(t *testing.T) {
	input := "\x1b_Ga=T,f=100,s=2,v=2,c=7,r=3,i=1,p=1,q=2,C=1,m=0;AAAA\x1b\\"
	var out bytes.Buffer
	w := &browserFrameWriter{out: &out}
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "a=t,f=100") || !strings.Contains(out.String(), "a=p,i=1,p=1,c=7,r=3,C=1,q=2") {
		t.Fatalf("single packet was not adapted: %q", out.String())
	}
}

type browserShortWriter struct{ n int }

func (w browserShortWriter) Write(p []byte) (int, error) { return min(w.n, len(p)), nil }

func TestBrowserFrameWriterReportsPartialWrites(t *testing.T) {
	for _, limit := range []int{0, len(browserSyncBegin) + 3, len(browserSyncBegin) + len(browserTestFrame)} {
		w := &browserFrameWriter{out: browserShortWriter{n: limit}}
		n, err := w.Write([]byte(browserTestFrame))
		want := max(0, min(len(browserTestFrame), limit-len(browserSyncBegin)))
		if n != want || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("limit=%d: Write = %d, %v; want %d and short write", limit, n, err, want)
		}
	}
}

func TestBrowserFrameWriterSerializesConcurrentWrites(t *testing.T) {
	var out bytes.Buffer
	w := &browserFrameWriter{out: &out}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				if _, err := w.Write([]byte(browserTestFrame)); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if got := strings.Count(out.String(), browserSyncBegin); got != 80 || strings.Count(out.String(), browserSyncEnd) != 80 {
		t.Fatalf("interleaved frames: starts=%d ends=%d", got, strings.Count(out.String(), browserSyncEnd))
	}
}
