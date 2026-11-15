# Keep the JNI entry points named. R8 sees the native methods, but an explicit
# rule documents the contract and survives future shrinker changes.
-keep class com.getan.golder.mobile.GolderNative { *; }
