package extensions

import "io"

// OfficialCLIInstallProgress contains no credentials, URLs or archive content.
// Total is zero when the server does not advertise a download size. Downloaded
// measures archive bytes, not verification or installation completion.
type OfficialCLIInstallProgress struct {
	Kind       OfficialCLIKind
	Phase      string // checking, connecting, downloading, verifying, extracting, installing, complete
	Downloaded int64
	Total      int64
}

type officialCLIProgressReader struct {
	reader io.Reader
	done   int64
	total  int64
	report func(int64, int64)
}

func (r *officialCLIProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.done += int64(n)
	if n > 0 && r.report != nil {
		r.report(r.done, r.total)
	}
	return n, err
}
