// abi_loader is a device-side smoke test for libgolder_mobile.so. It loads the
// Go shared library directly (not through a JVM), checks the exported C ABI,
// opens a file-only session, reads a snapshot, and closes it. It never starts a
// model request, so it needs no network or credential.
#include <dlfcn.h>
#include <stdio.h>
#include <string.h>

typedef char *(*version_fn)(void);
typedef char *(*last_error_fn)(void);
typedef int (*open_fn)(const char *);
typedef int (*start_fn)(int, const char *);
typedef char *(*poll_fn)(int, int);
typedef char *(*snapshot_fn)(int);
typedef void (*cancel_fn)(int);
typedef void (*close_fn)(int);
typedef void (*free_fn)(char *);

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: %s /path/to/libgolder_mobile.so\n", argv[0]);
		return 2;
	}
	void *lib = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
	if (lib == NULL) {
		fprintf(stderr, "dlopen: %s\n", dlerror());
		return 2;
	}
	version_fn version = (version_fn)dlsym(lib, "golder_mobile_version");
	last_error_fn last_error = (last_error_fn)dlsym(lib, "golder_mobile_last_error");
	open_fn open_session = (open_fn)dlsym(lib, "golder_mobile_open");
	start_fn start = (start_fn)dlsym(lib, "golder_mobile_start");
	poll_fn poll = (poll_fn)dlsym(lib, "golder_mobile_poll");
	snapshot_fn snapshot = (snapshot_fn)dlsym(lib, "golder_mobile_snapshot");
	cancel_fn cancel = (cancel_fn)dlsym(lib, "golder_mobile_cancel");
	close_fn close_session = (close_fn)dlsym(lib, "golder_mobile_close");
	free_fn free_string = (free_fn)dlsym(lib, "golder_mobile_free");
	if (!version || !last_error || !open_session || !start || !poll || !snapshot ||
	    !cancel || !close_session || !free_string) {
		fprintf(stderr, "missing exported symbol: %s\n", dlerror());
		return 2;
	}

	char *v = version();
	if (v == NULL) {
		fprintf(stderr, "version returned NULL\n");
		return 1;
	}
	printf("version=%s\n", v);
	free_string(v);

	// Empty model is rejected before any provider or file work. The secret in
	// this config must not appear in the error.
	int bad = open_session("{\"workspace\":\"/data/local/tmp\",\"model\":\"\",\"apiKey\":\"sk-abi-secret\"}");
	char *err = last_error();
	if (bad != 0 || err == NULL || strstr(err, "sk-abi-secret") != NULL) {
		fprintf(stderr, "invalid config was not rejected safely: handle=%d error=%s\n", bad, err ? err : "(null)");
		return 1;
	}
	printf("rejected=%s\n", err);
	free_string(err);

	// A valid file-only session can be opened without a network request. The
	// provider is resolved lazily; no stream is built until start.
	int handle = open_session("{\"workspace\":\"/data/local/tmp\",\"model\":\"gpt-4o-mini\"}");
	if (handle == 0) {
		err = last_error();
		fprintf(stderr, "open valid config failed: %s\n", err ? err : "(null)");
		free_string(err);
		return 1;
	}
	char *snap = snapshot(handle);
	if (snap == NULL || strstr(snap, "\"messages\"") == NULL) {
		fprintf(stderr, "snapshot is not the expected JSON: %s\n", snap ? snap : "(null)");
		free_string(snap);
		close_session(handle);
		return 1;
	}
	printf("handle=%d snapshot-bytes=%zu\n", handle, strlen(snap));
	free_string(snap);
	cancel(handle);
	close_session(handle);
	printf("abi-ok\n");
	return 0;
}
