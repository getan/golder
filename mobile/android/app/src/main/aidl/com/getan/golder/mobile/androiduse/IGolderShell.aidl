package com.getan.golder.mobile.androiduse;

import android.view.Surface;

// Restricted helper surface. This is intentionally much narrower than
// arbitrary shell execution; the Go policy layer decides every operation.
interface IGolderShell {
    int createDisplay(in Surface surface, int width, int height, int dpi);
    void releaseDisplay(int displayId);
    int startApp(int displayId, String packageName);
    boolean injectTap(int displayId, int x, int y);
    boolean injectKey(int displayId, int keyCode);
    void releaseAll();
    void destroy();
    boolean injectStroke(int displayId, in int[] xs, in int[] ys, in long[] times);
}
